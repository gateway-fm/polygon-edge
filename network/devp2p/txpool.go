package devp2p

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type NoopTxPool struct {
}

func (t *NoopTxPool) Get(hash common.Hash) *types.Transaction {
	return nil
}
