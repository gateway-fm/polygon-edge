package devp2p

import (
	"math/big"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/types"
)

type NodeInfo struct {
	Network    uint64        `json:"network"`
	Difficulty *big.Int      `json:"difficulty"`
	Genesis    types.Hash    `json:"genesis"`
	Config     *chain.Params `json:"config"`
	Head       types.Hash    `json:"head"`
}
