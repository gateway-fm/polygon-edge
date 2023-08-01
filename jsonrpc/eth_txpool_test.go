package jsonrpc

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/state/runtime/tracer"
	"github.com/0xPolygon/polygon-edge/types"
)

func TestEth_TxnPool_SendRawTransaction(t *testing.T) {
	store := &mockStoreTxn{}
	eth := newTestEthEndpoint(store)

	txn := &types.Transaction{
		From: addr0,
		V:    big.NewInt(1),
	}
	txn.ComputeHash(1)

	data := txn.MarshalRLP()
	_, err := eth.SendRawTransaction(data)
	assert.NoError(t, err)
	assert.NotEqual(t, store.txn.Hash, types.ZeroHash)

	// the hash in the txn pool should match the one we send
	if txn.Hash != store.txn.Hash {
		t.Fatal("bad")
	}
}

func TestEth_TxnPool_SendTransaction(t *testing.T) {
	store := &mockStoreTxn{}
	store.AddAccount(addr0)
	eth := newTestEthEndpoint(store)

	txToSend := &types.Transaction{
		From:     addr0,
		To:       argAddrPtr(addr0),
		Nonce:    uint64(0),
		GasPrice: big.NewInt(int64(1)),
	}

	_, err := eth.SendRawTransaction(txToSend.MarshalRLP())
	assert.NoError(t, err)
	assert.NotEqual(t, store.txn.Hash, types.ZeroHash)
}

type mockStoreTxn struct {
	EthStore
	accounts map[types.Address]*mockAccount
	txn      *types.Transaction
}

func (m *mockStoreTxn) GetPeers() int {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) GetTxs(inclQueued bool) (map[types.Address][]*types.Transaction, map[types.Address][]*types.Transaction) {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) GetCapacity() (uint64, uint64) {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) GetBaseFee() uint64 {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) SubscribeEvents() blockchain.Subscription {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) TraceBlock(t *types.Block, tracer tracer.Tracer) ([]interface{}, error) {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) TraceTxn(t *types.Block, hash types.Hash, tracer tracer.Tracer) (interface{}, error) {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) TraceCall(t *types.Transaction, header *types.Header, tracer tracer.Tracer) (interface{}, error) {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) BridgeDataProvider() consensus.BridgeDataProvider {
	//TODO implement me
	panic("implement me")
}

func (m *mockStoreTxn) AddTx(tx *types.Transaction) error {
	m.txn = tx

	tx.ComputeHash(1)

	return nil
}

func (m *mockStoreTxn) GetNonce(addr types.Address) uint64 {
	return 1
}

func (m *mockStoreTxn) AddAccount(addr types.Address) *mockAccount {
	if m.accounts == nil {
		m.accounts = map[types.Address]*mockAccount{}
	}

	acct := &mockAccount{
		address: addr,
		account: &Account{},
		storage: make(map[types.Hash][]byte),
	}
	m.accounts[addr] = acct

	return acct
}

func (m *mockStoreTxn) Header() *types.Header {
	return &types.Header{}
}

func (m *mockStoreTxn) GetAccount(root types.Hash, addr types.Address) (*Account, error) {
	acct, ok := m.accounts[addr]
	if !ok {
		return nil, ErrStateNotFound
	}

	return acct.account, nil
}
