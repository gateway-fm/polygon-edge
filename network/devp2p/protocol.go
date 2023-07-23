package devp2p

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/types"
)

const (
	ETH66 = 66
	ETH67 = 67
	ETH68 = 68
)

// ProtocolName is the official short name of the `eth` protocol used during
// devp2p capability negotiation.
const ProtocolName = "eth"

// ProtocolVersions are the supported versions of the `eth` protocol (first
// is primary).
var ProtocolVersions = []uint{ETH68, ETH67, ETH66}

// protocolLengths are the number of implemented message corresponding to
// different protocol versions.
var protocolLengths = map[uint]uint64{ETH68: 17, ETH67: 17, ETH66: 17}

// maxMessageSize is the maximum cap on the size of a protocol message.
const maxMessageSize = 10 * 1024 * 1024

const (
	StatusMsg                     = 0x00
	NewBlockHashesMsg             = 0x01
	TransactionsMsg               = 0x02
	GetBlockHeadersMsg            = 0x03
	BlockHeadersMsg               = 0x04
	GetBlockBodiesMsg             = 0x05
	BlockBodiesMsg                = 0x06
	NewBlockMsg                   = 0x07
	GetNodeDataMsg                = 0x0d
	NodeDataMsg                   = 0x0e
	GetReceiptsMsg                = 0x0f
	ReceiptsMsg                   = 0x10
	NewPooledTransactionHashesMsg = 0x08
	GetPooledTransactionsMsg      = 0x09
	PooledTransactionsMsg         = 0x0a
)

// StatusPacket is the network packet for the status message for eth/64 and later.
type StatusPacket struct {
	ProtocolVersion uint32
	NetworkID       uint64
	TD              *big.Int
	Head            types.Hash
	Genesis         types.Hash
	ForkID          forkid.ID
}

type msgHandler func(blockchain *blockchain.Blockchain, msg eth.Decoder, peer *Peer) error

var eth66 = map[uint64]msgHandler{
	GetBlockHeadersMsg: handleBlockHeaders66,
	GetBlockBodiesMsg:  handleGetBlockBodies66,
	NewBlockMsg:        handleNewBlockMessage,
	BlockHeadersMsg:    handleBlockHeadersMessage,
	BlockBodiesMsg:     handleBlockBodiesMessage,
}

var eth67 = map[uint64]msgHandler{
	GetBlockHeadersMsg: handleBlockHeaders66,
	GetBlockBodiesMsg:  handleGetBlockBodies66,
	NewBlockMsg:        handleNewBlockMessage,
	BlockHeadersMsg:    handleBlockHeadersMessage,
	BlockBodiesMsg:     handleBlockBodiesMessage,
}

var eth68 = map[uint64]msgHandler{
	GetBlockHeadersMsg: handleBlockHeaders66,
	GetBlockBodiesMsg:  handleGetBlockBodies66,
	NewBlockMsg:        handleNewBlockMessage,
	BlockHeadersMsg:    handleBlockHeadersMessage,
	BlockBodiesMsg:     handleBlockBodiesMessage,
}

func handleBlockHeaders66(bc *blockchain.Blockchain, msg eth.Decoder, peer *Peer) error {
	var q eth.GetBlockHeadersPacket66
	err := msg.Decode(&q)
	if err != nil {
		return err
	}
	headers := make([]*types.Header, 0)
	if q.Origin.Hash != (common.Hash{}) {
		h := types.BytesToHash(q.Origin.Hash.Bytes())
		b, found := bc.GetBlockByHash(h, true)
		if !found {
			return peer.ReplyEmptyBlockHeaders66(q.RequestId)
		}
		headers = append(headers, b.Header)
	} else {
		b, found := bc.GetBlockByNumber(q.Origin.Number, true)
		if !found {
			return peer.ReplyEmptyBlockHeaders66(q.RequestId)
		}
		headers = append(headers, b.Header)
	}
	return peer.ReplyBlockHeaders66(q.RequestId, headers)
}

func handleGetBlockBodies66(bc *blockchain.Blockchain, msg eth.Decoder, peer *Peer) error {
	var q eth.GetBlockBodiesPacket66
	err := msg.Decode(&q)
	if err != nil {
		return err
	}
	return peer.ReplyEmptyBlockBodies66(q.RequestId)
}

func handleNewBlockMessage(bc *blockchain.Blockchain, msg eth.Decoder, peer *Peer) error {
	var q eth.NewBlockPacket
	err := msg.Decode(&q)
	if err != nil {
		return err
	}
	num := q.Block.NumberU64()
	peer.highestBlock = num
	bc.NewBlockNumberAnnounced(num)
	return nil
}

func handleBlockHeadersMessage(bc *blockchain.Blockchain, msg eth.Decoder, peer *Peer) error {
	res := new(eth.BlockHeadersPacket66)
	if err := msg.Decode(res); err != nil {
		return fmt.Errorf("error decoding block headers message: %w", err)
	}
	response := &PeerResponse{
		Id:   res.RequestId,
		Code: BlockHeadersMsg,
		Res:  res,
		Peer: peer,
	}
	return peer.DispatchResponse(response)
}

func handleBlockBodiesMessage(bc *blockchain.Blockchain, msg eth.Decoder, peer *Peer) error {
	res := new(eth.BlockBodiesPacket66)
	if err := msg.Decode(res); err != nil {
		return fmt.Errorf("error decoding block bodies message: %w", err)
	}
	response := &PeerResponse{
		Id:   res.RequestId,
		Code: BlockBodiesMsg,
		Res:  res,
		Peer: peer,
	}
	return peer.DispatchResponse(response)
}
