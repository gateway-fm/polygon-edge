package devp2p

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/0xPolygon/polygon-edge/types"
)

const (
	// handshakeTimeout is the maximum allowed time for the `eth` handshake to
	// complete before dropping the connection.= as malicious.
	handshakeTimeout = 5 * time.Second
)

var (
	errorDisconnected = errors.New("the peer has been disconnected")
)

type Peer struct {
	id              string
	version         uint
	underlyingPeer  *p2p.Peer
	readWriter      p2p.MsgReadWriter
	td              *big.Int
	head            types.Hash
	terminate       chan struct{}
	openRequests    map[uint64]*PeerRequest
	openRequestsMtx *sync.Mutex
	requests        atomic.Int64
	isReady         atomic.Bool
	highestBlock    uint64
}

func NewPeer(id string, version uint, underlyingPeer *p2p.Peer, readWriter p2p.MsgReadWriter, td *big.Int) *Peer {
	return &Peer{
		id:              id,
		version:         version,
		underlyingPeer:  underlyingPeer,
		readWriter:      readWriter,
		td:              td,
		head:            types.Hash{},
		terminate:       make(chan struct{}),
		openRequests:    make(map[uint64]*PeerRequest),
		openRequestsMtx: &sync.Mutex{},
	}
}

func (p *Peer) Version() uint {
	return p.version
}

func (p *Peer) Handshake(network uint64, td *big.Int, head types.Hash, genesis types.Hash, forkID forkid.ID, forkFilter forkid.Filter) error {
	errc := make(chan error, 2)

	var status StatusPacket // safe to read after two values have been received from errc

	go func() {
		errc <- p2p.Send(p.readWriter, StatusMsg, &StatusPacket{
			ProtocolVersion: uint32(p.version),
			NetworkID:       network,
			TD:              td,
			Head:            head,
			Genesis:         genesis,
			ForkID:          forkID,
		})
	}()
	go func() {
		errc <- p.readStatus(network, &status, genesis, forkFilter)
	}()
	timeout := time.NewTimer(handshakeTimeout)
	defer timeout.Stop()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err != nil {
				return err
			}
		case <-timeout.C:
			return p2p.DiscReadTimeout
		}
	}
	p.td, p.head = status.TD, status.Head

	// terminal difficulty is block height + 1
	p.highestBlock = p.td.Uint64() - 1

	p.isReady.Store(true)

	return nil
}

func (p *Peer) ReplyEmptyBlockHeaders66(id uint64) error {
	return p2p.Send(p.readWriter, BlockHeadersMsg, &eth.BlockHeadersRLPPacket66{
		RequestId:             id,
		BlockHeadersRLPPacket: []rlp.RawValue{},
	})
}

func (p *Peer) ReplyBlockHeaders66(id uint64, headers []*types.Header) error {
	values := make([]rlp.RawValue, 0)
	for _, h := range headers {
		values = append(values, h.MarshalRLP())
	}
	return p2p.Send(p.readWriter, BlockHeadersMsg, &eth.BlockHeadersRLPPacket66{
		RequestId:             id,
		BlockHeadersRLPPacket: values,
	})
}

func (p *Peer) ReplyEmptyBlockBodies66(id uint64) error {
	return p2p.Send(p.readWriter, BlockBodiesMsg, &eth.BlockBodiesRLPPacket66{
		RequestId:            id,
		BlockBodiesRLPPacket: []rlp.RawValue{},
	})
}

func (p *Peer) SendGetBlockHeadersPacket66(packet *eth.GetBlockHeadersPacket, sink chan *PeerResponse) (uint64, error) {
	req := &PeerRequest{
		Id:   p.getNextRequestNumber(),
		Code: BlockHeadersMsg,
		Sink: sink,
	}

	p.openRequestsMtx.Lock()
	p.openRequests[req.Id] = req
	p.openRequestsMtx.Unlock()

	return req.Id, p2p.Send(p.readWriter, GetBlockHeadersMsg, &eth.GetBlockHeadersPacket66{
		RequestId:             req.Id,
		GetBlockHeadersPacket: packet,
	})
}

func (p *Peer) SendGetBlockBodiesPacket66(packet eth.GetBlockBodiesPacket, sink chan *PeerResponse) (uint64, error) {
	req := &PeerRequest{
		Id:   p.getNextRequestNumber(),
		Code: BlockBodiesMsg,
		Sink: sink,
	}

	p.openRequestsMtx.Lock()
	p.openRequests[req.Id] = req
	p.openRequestsMtx.Unlock()

	return req.Id, p2p.Send(p.readWriter, GetBlockBodiesMsg, &eth.GetBlockBodiesPacket66{
		RequestId:            req.Id,
		GetBlockBodiesPacket: packet,
	})
}

func (p *Peer) getNextRequestNumber() uint64 {
	p.requests.Add(1)
	no := p.requests.Load()
	return uint64(no)
}

func (p *Peer) DispatchResponse(res *PeerResponse) error {
	select {
	case <-p.terminate:
		return errorDisconnected
	default:
		id := res.Id
		p.openRequestsMtx.Lock()
		found, ok := p.openRequests[id]
		p.openRequestsMtx.Unlock()
		if !ok {
			return fmt.Errorf("could not find matching request for peer %v", id)
		}

		if found.Code != res.Code {
			return fmt.Errorf("a peer response message code did not match, found: %v, wanted: %v", res.Code, found.Code)
		}

		found.Sink <- res

		p.openRequestsMtx.Lock()
		delete(p.openRequests, id)
		p.openRequestsMtx.Unlock()

		return nil
	}
}

// CanRequest checks if the peer has any available requests remaining.
// only a certain number of open requests can exist at any given moment
func (p *Peer) CanRequest() bool {
	p.openRequestsMtx.Lock()
	defer p.openRequestsMtx.Unlock()

	ready := p.isReady.Load()
	if !ready {
		return false
	}

	return len(p.openRequests) < 3
}

func (p *Peer) ResponseReceived(id uint64) {
	p.openRequestsMtx.Lock()
	defer p.openRequestsMtx.Unlock()
	delete(p.openRequests, id)
}

// readStatus reads the remote handshake message.
func (p *Peer) readStatus(network uint64, status *StatusPacket, genesis types.Hash, forkFilter forkid.Filter) error {
	msg, err := p.readWriter.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != StatusMsg {
		return fmt.Errorf("%w: first msg has code %x (!= %x)", errNoStatusMsg, msg.Code, StatusMsg)
	}
	if msg.Size > maxMessageSize {
		return fmt.Errorf("%w: %v > %v", errMsgTooLarge, msg.Size, maxMessageSize)
	}
	// Decode the handshake and make sure everything matches
	if err := msg.Decode(&status); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if status.NetworkID != network {
		return fmt.Errorf("%w: %d (!= %d)", errNetworkIDMismatch, status.NetworkID, network)
	}
	if uint(status.ProtocolVersion) != p.version {
		return fmt.Errorf("%w: %d (!= %d)", errProtocolVersionMismatch, status.ProtocolVersion, p.version)
	}
	if status.Genesis != genesis {
		return fmt.Errorf("%w: %x (!= %x)", errGenesisMismatch, status.Genesis, genesis)
	}
	if err := forkFilter(status.ForkID); err != nil {
		return fmt.Errorf("%w: %v", errForkIDRejected, err)
	}
	return nil
}

var (
	errNoStatusMsg             = errors.New("no status message")
	errMsgTooLarge             = errors.New("message too long")
	errDecode                  = errors.New("invalid message")
	errInvalidMsgCode          = errors.New("invalid message code")
	errProtocolVersionMismatch = errors.New("protocol version mismatch")
	errNetworkIDMismatch       = errors.New("network ID mismatch")
	errGenesisMismatch         = errors.New("genesis mismatch")
	errForkIDRejected          = errors.New("fork ID rejected")
)

type peerInfo struct {
	Version uint `json:"version"`
}

type PeerResponse struct {
	Id   uint64
	Code int
	Res  interface{}
	Peer *Peer
}

type PeerRequest struct {
	Id   uint64
	Code int
	Sink chan *PeerResponse
}
