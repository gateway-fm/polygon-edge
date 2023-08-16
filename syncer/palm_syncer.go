package syncer

import (
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/fork"
	"github.com/0xPolygon/polygon-edge/helper/progress"
	devp2p2 "github.com/0xPolygon/polygon-edge/network/devp2p"
	"github.com/0xPolygon/polygon-edge/types"
)

const (
	requestLoopLimit          = 10
	maxBlocksInRequest        = 128
	maxHeightFromStoredBlocks = 2000
	palmSyncerName            = "palm_syncer"
)

var (
	noAvailablePeersErr = fmt.Errorf("no available peers")
)

type openHeadersRequest struct {
	expires        time.Time
	includedBlocks []uint64
	request        *eth.GetBlockHeadersPacket
}

type openBodiesRequest struct {
	expires        time.Time
	includedBlocks []blockNumberWithHash
	request        *eth.GetBlockBodiesPacket
	requestId      uint64
}

type blockNumberWithHash struct {
	blockNumber uint64
	hash        types.Hash
}

type PalmSyncer struct {
	logger      hclog.Logger
	config      *consensus.Config
	blockchain  *blockchain.Blockchain
	devp2p      *devp2p2.Server
	forkManager *fork.ForkManager

	headersMtx     *sync.Mutex
	bodiesMtx      *sync.Mutex
	headers        map[uint64]*types.Header
	bodies         map[uint64]*types.Body
	openHeaders    map[uint64]*openHeadersRequest
	openHeadersMtx *sync.Mutex
	openBodies     map[uint64]*openBodiesRequest
	openBodiesMtx  *sync.Mutex

	stop      chan struct{}
	fromPeers chan *devp2p2.PeerResponse
}

func NewPalmSyncer(logger hclog.Logger, cfg *consensus.Config, bc *blockchain.Blockchain, forkManager *fork.ForkManager) *PalmSyncer {
	return &PalmSyncer{
		logger:         logger,
		config:         cfg,
		blockchain:     bc,
		forkManager:    forkManager,
		headersMtx:     &sync.Mutex{},
		bodiesMtx:      &sync.Mutex{},
		bodies:         map[uint64]*types.Body{},
		headers:        map[uint64]*types.Header{},
		openHeaders:    map[uint64]*openHeadersRequest{},
		openHeadersMtx: &sync.Mutex{},
		openBodies:     map[uint64]*openBodiesRequest{},
		openBodiesMtx:  &sync.Mutex{},
		stop:           make(chan struct{}),
		fromPeers:      make(chan *devp2p2.PeerResponse, 1024),
	}
}

func (p *PalmSyncer) Start() error {
	peers, ok := p.config.Config["devp2p-peers"]
	if !ok {
		return fmt.Errorf("expected devp2p-peers key to be present in consensus config")
	}

	addr, ok := p.config.Config["devp2p-addr"]
	if !ok {
		return fmt.Errorf("expected devp2p-addr key to be present in consensus config")
	}
	netAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("expected the devp2p-addr config to be a net.TCPAddr type")
	}

	cfg := &devp2p2.Config{
		StaticPeers:   peers.([]string),
		ListenAddress: netAddr.String(),
		Blockchain:    p.blockchain,
	}

	p2p, err := devp2p2.NewServer(p.logger, cfg, p.blockchain)
	if err != nil {
		return err
	}

	err = p2p.Start()
	if err != nil {
		return err
	}

	p.devp2p = p2p

	p.logger.Info("palm syncer started using devp2p")

	return nil
}

func (p *PalmSyncer) Close() error {
	close(p.stop)
	p.devp2p.Stop()
	p.logger.Info("palm syncer closed")
	return nil
}

func (p *PalmSyncer) GetSyncProgression() *progress.Progression {
	p.logger.Info("palm syncer getting sync progress")
	return &progress.Progression{}
}

func (p *PalmSyncer) HasSyncPeer() bool {
	p.logger.Info("palm syncer has sync peer called")
	return true
}

func (p *PalmSyncer) Sync(callback func(*types.FullBlock) bool) error {
	p.logger.Info("palm syncer asked to sync")

	// start up the routines to request bodies and headers
	go p.monitorForPeerResponses()

	logTicker := time.NewTicker(5 * time.Second)
	summaryTicker := time.NewTicker(1 * time.Minute)
	summaryCount := 0
	localLatest := p.blockchain.Header().Number
	isHeaders := true
	currentBlock := localLatest

LOOP:
	for {
		select {
		case <-p.stop:
			break LOOP
		case <-logTicker.C:
			p.logger.Info("palm syncer loop running", "currentBlock", currentBlock, "openHeaders", len(p.openHeaders), "openBodies", len(p.openBodies))
		case <-summaryTicker.C:
			p.logger.Info("palm syncer summary", "synced-in-window", summaryCount)
			summaryCount = 0
		default:
		}

		// send requests until we can't send more - alternating between requesting headers
		// and bodies
		known := p.blockchain.GetHighestKnownNumber()
		if known == 0 {
			// ensure we know how high of a block we need to sync to before starting
			time.Sleep(10 * time.Millisecond)
			continue
		}

		requestCount := 0
		if isHeaders {
			for {
				sent := p.requestNextHeaders(known, currentBlock)
				requestCount++
				if !sent || requestCount > requestLoopLimit {
					break
				}
			}
		} else {
			for {
				sent := p.requestNextBodies(known, currentBlock)
				requestCount++
				if !sent || requestCount > requestLoopLimit {
					break
				}
			}
		}

		isHeaders = !isHeaders

		next := currentBlock + 1

		for {
			select {
			case <-p.stop:
				break LOOP
			default:
			}

			header, headOk := p.getHeader(next)
			if !headOk {
				break
			}

			body, bodyOk := p.getBody(next)
			if !bodyOk {
				break
			}

			if headOk && bodyOk {

				// Todo [palm] - remove this - purely for faster breakpoints while debugging
				if header.Number == 2111200 {
					foo := 1
					_ = foo
				}

				block := &types.Block{
					Header:       header,
					Transactions: body.Transactions,
					Uncles:       body.Uncles,
				}

				fullBlock, err := p.blockchain.VerifyFinalizedBlock(block)
				if err != nil {
					p.logger.Error("error verifying finalized block", "no", block.Number(), "err", err)
					break
				}

				err = p.blockchain.WriteFullBlock(fullBlock, palmSyncerName)
				if err != nil {
					p.logger.Error("error writing full block", "no", block.Number(), "err", err)
					break
				}

				// now delete the header/body from the returned lists
				p.deleteHeader(next)
				p.deleteBody(next)

				summaryCount++
				next++
				currentBlock++

				select {
				case <-p.stop:
					break LOOP
				default:
				}

				if callback(fullBlock) {
					break LOOP
				}
			}
		}
	}

	return nil
}

func (p *PalmSyncer) requestNextHeaders(knownHeight uint64, current uint64) bool {
	// first remove any aged header requests
	p.pruneAgedHeaderRequests()

	if current == knownHeight {
		// no new blocks since the last loop
		return false
	}

	ceiling := current + maxHeightFromStoredBlocks
	nextBlock := current + 1

	// increase by headers we might already have
	highestHeader := p.getHighestHeaderNumber()
	if highestHeader >= knownHeight {
		return false
	}
	if highestHeader > nextBlock {
		nextBlock = highestHeader + 1
	}

	highestRequestedHeader := p.getHighestRequestedHeader()
	if highestRequestedHeader >= knownHeight {
		return false
	}
	if highestRequestedHeader > nextBlock {
		nextBlock = highestRequestedHeader
	}

	// if the next request goes above the ceiling then we don't need to request
	if nextBlock > ceiling {
		return false
	}

	// work out how many headers we need to ask for in this request capped by
	// maxBlocksInRequest
	amountToRequest := ceiling - current
	if amountToRequest > maxBlocksInRequest {
		amountToRequest = maxBlocksInRequest
	}

	if amountToRequest == 0 {
		return false
	}

	packet := &eth.GetBlockHeadersPacket{
		Origin:  eth.HashOrNumber{Number: nextBlock},
		Amount:  amountToRequest,
		Skip:    0,
		Reverse: false,
	}

	peer := p.devp2p.GetAvailablePeer()
	if peer == nil {
		return false
	}

	reqId, err := peer.SendGetBlockHeadersPacket66(packet, p.fromPeers)
	if err != nil {
		p.logger.Error("error sending get block headers message to peer", "err", err)
		return false
	}

	or := &openHeadersRequest{
		expires: time.Now().Add(time.Second * 2),
		request: packet,
	}

	p.setOpenHeaderRequest(reqId, or)

	p.logger.Debug("sent get block headers message to peer", "reqId", reqId, "origin", packet.Origin.Number, "amount", packet.Amount, "skip", packet.Skip)

	return true
}

func (p *PalmSyncer) requestNextBodies(maxHeight uint64, current uint64) bool {
	p.pruneAgedBodiesRequests()

	var thisRequest []common.Hash
	var batch []blockNumberWithHash
	var numbersInRequest []uint64

	// build up a request based on headers we already have in place and open body requests
	nextBlock := current + 1

	var i uint64
	for i = 0; i < maxHeightFromStoredBlocks; i++ {
		// do we have the header?
		header, ok := p.getHeader(nextBlock + i)
		if !ok {
			// only request contiguous blocks of numbers
			break
		}

		if p.hasOpenBodyRequest(nextBlock + i) {
			continue
		}

		// this is a body we need to request
		thisRequest = append(thisRequest, devp2p2.EdgeHashToGethHash(header.Hash))
		batch = append(batch, blockNumberWithHash{
			blockNumber: nextBlock + i,
			hash:        header.Hash,
		})
		numbersInRequest = append(numbersInRequest, nextBlock+i)

		if len(thisRequest) >= maxBlocksInRequest {
			break
		}
	}

	if len(thisRequest) == 0 {
		return false
	}

	packet := eth.GetBlockBodiesPacket(thisRequest)

	// try and get a free peer and just quit early if they are all busy
	peer := p.devp2p.GetAvailablePeer()
	if peer == nil {
		return false
	}

	// try a few attempts if we encounter an error, could be transient
	var reqId uint64
	var err error
	for i := 0; i < 10; i++ {
		reqId, err = peer.SendGetBlockBodiesPacket66(packet, p.fromPeers)
		if err != nil {
			p.logger.Error("error from peer", "err", err)
			time.Sleep(10 * time.Millisecond)
			// TODO - how can we handle this better?
		} else {
			break
		}
	}

	if err != nil {
		p.logger.Error("error sending get block bodies message to peer", "err", err)
		return false
	}

	or := &openBodiesRequest{
		expires:        time.Now().Add(time.Second * 2),
		includedBlocks: batch,
		request:        &packet,
		requestId:      reqId,
	}
	p.setOpenBodiesRequest(reqId, or)

	p.logger.Debug("sent get block bodies message to peer", "reqId", reqId, "count", len(numbersInRequest), "blocks", numbersInRequest)

	return true
}

func (p *PalmSyncer) pruneAgedHeaderRequests() {
	p.openHeadersMtx.Lock()
	defer p.openHeadersMtx.Unlock()

	now := time.Now()

	for idx, r := range p.openHeaders {
		if now.After(r.expires) {
			delete(p.openHeaders, idx)
		}
	}
}

func (p *PalmSyncer) pruneAgedBodiesRequests() {
	p.openBodiesMtx.Lock()
	defer p.openBodiesMtx.Unlock()

	now := time.Now()

	for idx, r := range p.openBodies {
		if now.After(r.expires) {
			delete(p.openBodies, idx)
		}
	}
}

func (p *PalmSyncer) monitorForPeerResponses() {
	for {
		select {
		case res := <-p.fromPeers:
			switch res.Code {
			case devp2p2.BlockHeadersMsg:
				convert, ok := res.Res.(*eth.BlockHeadersPacket66)
				if !ok {
					p.logger.Error("unexpected type when handling a block headers response", "type", reflect.TypeOf(res.Res))
					continue
				}
				p.logger.Debug("got block headers response from peer", "reqId", convert.RequestId, "count", len(convert.BlockHeadersPacket))
				p.handleHeaderPeerResponse(convert)

			case devp2p2.BlockBodiesMsg:
				convert, ok := res.Res.(*eth.BlockBodiesPacket66)
				if !ok {
					p.logger.Error("unexpected type when handling a block bodies response", "type", reflect.TypeOf(res.Res))
					continue
				}
				p.handleBodiesPeerResponse(convert)
				p.logger.Debug("got block bodies response from peer", "reqId", convert.RequestId, "count", len(convert.BlockBodiesPacket))
			default:
				p.logger.Error("monitoring for peer responses got an unexpected message code", "code", res.Code)
			}
		case <-p.stop:
			return
		}
	}
}

func (p *PalmSyncer) handleHeaderPeerResponse(packet *eth.BlockHeadersPacket66) {
	for _, h := range packet.BlockHeadersPacket {
		header, err := devp2p2.GethHeaderConvert(h)
		header.ComputeHash()
		if err != nil {
			p.logger.Error("error converting header from p2p response", "err", err)
		}

		p.addHeader(header.Number, header)
	}

	p.deleteOpenHeaderRequest(packet.RequestId)
}

func (p *PalmSyncer) handleBodiesPeerResponse(packet *eth.BlockBodiesPacket66) {
	correspondingRequest, found := p.getOpenBodyRequest(packet.RequestId)
	if !found {
		p.logger.Warn("received a bodies response that we didn't expect, ignoring response", "id", packet.RequestId)
		return
	}

	if len(correspondingRequest.includedBlocks) != len(packet.BlockBodiesPacket) {
		p.logger.Warn(
			"received a bodies response with differing counts to the original request",
			"original",
			len(correspondingRequest.includedBlocks),
			"received",
			len(packet.BlockBodiesPacket),
		)
	}

	for idx, b := range packet.BlockBodiesPacket {
		var transactions []*types.Transaction
		for _, t := range b.Transactions {
			tx := devp2p2.GethTransactionConvert(t)
			transactions = append(transactions, tx)
		}

		var uncles []*types.Header
		for _, u := range b.Uncles {
			// ignoring the hash here as we'd need to know chain height at this point
			// possibly one for the future if this becomes necessary
			uh := devp2p2.GethHeaderConvertNoHash(u)
			uncles = append(uncles, uh)
		}

		body := &types.Body{
			Transactions: transactions,
			Uncles:       uncles,
		}

		cr := correspondingRequest.includedBlocks[idx]

		p.addBody(cr.blockNumber, body)
	}

	p.deleteOpenBodiesRequest(packet.RequestId)
}

func (p *PalmSyncer) getHighestHeaderNumber() uint64 {
	p.headersMtx.Lock()
	defer p.headersMtx.Unlock()
	var high uint64 = 0
	for _, h := range p.headers {
		if h.Number > high {
			high = h.Number
		}
	}

	return high
}

func (p *PalmSyncer) getHighestRequestedHeader() uint64 {
	p.openHeadersMtx.Lock()
	defer p.openHeadersMtx.Unlock()

	high := uint64(0)
	for _, r := range p.openHeaders {
		height := r.request.Origin.Number + r.request.Amount
		if height > high {
			high = height
		}
	}

	return high
}

func (p *PalmSyncer) getHeader(block uint64) (*types.Header, bool) {
	p.headersMtx.Lock()
	defer p.headersMtx.Unlock()
	h, ok := p.headers[block]
	return h, ok
}

func (p *PalmSyncer) getBody(block uint64) (*types.Body, bool) {
	p.bodiesMtx.Lock()
	defer p.bodiesMtx.Unlock()
	b, ok := p.bodies[block]
	return b, ok
}

func (p *PalmSyncer) addHeader(block uint64, header *types.Header) {
	p.headersMtx.Lock()
	defer p.headersMtx.Unlock()
	p.headers[block] = header
}

func (p *PalmSyncer) addBody(block uint64, body *types.Body) {
	p.bodiesMtx.Lock()
	defer p.bodiesMtx.Unlock()
	p.bodies[block] = body
}

func (p *PalmSyncer) deleteHeader(block uint64) {
	p.headersMtx.Lock()
	defer p.headersMtx.Unlock()
	delete(p.headers, block)
}

func (p *PalmSyncer) deleteBody(block uint64) {
	p.bodiesMtx.Lock()
	defer p.bodiesMtx.Unlock()
	delete(p.bodies, block)
}

func (p *PalmSyncer) hasOpenBodyRequest(block uint64) bool {
	p.openBodiesMtx.Lock()
	defer p.openBodiesMtx.Unlock()

	for _, ob := range p.openBodies {
		for _, x := range ob.includedBlocks {
			if x.blockNumber == block {
				return true
			}
		}
	}

	return false
}

func (p *PalmSyncer) getOpenBodyRequest(id uint64) (*openBodiesRequest, bool) {
	p.openBodiesMtx.Lock()
	defer p.openBodiesMtx.Unlock()
	r, ok := p.openBodies[id]
	return r, ok
}

func (p *PalmSyncer) setOpenBodiesRequest(id uint64, req *openBodiesRequest) {
	p.openBodiesMtx.Lock()
	defer p.openBodiesMtx.Unlock()
	p.openBodies[id] = req
}

func (p *PalmSyncer) deleteOpenBodiesRequest(id uint64) {
	p.openBodiesMtx.Lock()
	defer p.openBodiesMtx.Unlock()
	delete(p.openBodies, id)
}

func (p *PalmSyncer) getOpenHeaderRequest(id uint64) (*openHeadersRequest, bool) {
	p.openHeadersMtx.Lock()
	defer p.openHeadersMtx.Unlock()
	r, ok := p.openHeaders[id]
	return r, ok
}

func (p *PalmSyncer) setOpenHeaderRequest(id uint64, request *openHeadersRequest) {
	p.openHeadersMtx.Lock()
	defer p.openHeadersMtx.Unlock()
	p.openHeaders[id] = request
}

func (p *PalmSyncer) deleteOpenHeaderRequest(id uint64) {
	p.openHeadersMtx.Lock()
	defer p.openHeadersMtx.Unlock()
	delete(p.openHeaders, id)
}
