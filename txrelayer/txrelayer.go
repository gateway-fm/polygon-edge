package txrelayer

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"time"

	"github.com/umbracle/ethgo"
	"github.com/umbracle/ethgo/jsonrpc"
	"github.com/umbracle/ethgo/wallet"
)

const (
	defaultGasPrice   = 1879048192 // 0x70000000
	DefaultGasLimit   = 5242880    // 0x500000
	DefaultRPCAddress = "http://127.0.0.1:8545"
	numRetries        = 1000
	gasLimitPercent   = 100
	gasPricePercent   = 20
)

var (
	errNoAccounts = errors.New("no accounts registered")
)

type TxRelayer interface {
	// Call executes a message call immediately without creating a transaction on the blockchain
	Call(from ethgo.Address, to ethgo.Address, input []byte) (string, error)
	// SendTransaction signs given transaction by provided key and sends it to the blockchain
	SendTransaction(txn *ethgo.Transaction, key ethgo.Key) (*ethgo.Receipt, error)
	// SendTransactionLocal sends non-signed transaction
	// (this function is meant only for testing purposes and is about to be removed at some point)
	SendTransactionLocal(txn *ethgo.Transaction) (*ethgo.Receipt, error)
	// Client returns jsonrpc client
	Client() *jsonrpc.Client
}

var _ TxRelayer = (*TxRelayerImpl)(nil)

type TxRelayerImpl struct {
	ipAddress      string
	client         *jsonrpc.Client
	receiptTimeout time.Duration

	lock sync.Mutex

	writer io.Writer

	useNonceMap bool
	nonceMap    map[ethgo.Address]uint64
	nonceMapMtx *sync.Mutex
}

func NewTxRelayer(opts ...TxRelayerOption) (TxRelayer, error) {
	t := &TxRelayerImpl{
		ipAddress:      DefaultRPCAddress,
		receiptTimeout: 1 * time.Second,
		useNonceMap:    false,
		nonceMapMtx:    &sync.Mutex{},
		nonceMap:       make(map[ethgo.Address]uint64),
	}
	for _, opt := range opts {
		opt(t)
	}

	if t.client == nil {
		client, err := jsonrpc.NewClient(t.ipAddress)
		if err != nil {
			return nil, err
		}

		t.client = client
	}

	return t, nil
}

// Call executes a message call immediately without creating a transaction on the blockchain
func (t *TxRelayerImpl) Call(from ethgo.Address, to ethgo.Address, input []byte) (string, error) {
	callMsg := &ethgo.CallMsg{
		From: from,
		To:   &to,
		Data: input,
	}

	return t.client.Eth().Call(callMsg, ethgo.Pending)
}

// SendTransaction signs given transaction by provided key and sends it to the blockchain
func (t *TxRelayerImpl) SendTransaction(txn *ethgo.Transaction, key ethgo.Key) (*ethgo.Receipt, error) {
	txnHash, err := t.sendTransactionLocked(txn, key)
	if err != nil {
		return nil, err
	}

	fmt.Println("Sent transaction with hash", txnHash)

	return t.waitForReceipt(txnHash)
}

// Client returns jsonrpc client
func (t *TxRelayerImpl) Client() *jsonrpc.Client {
	return t.client
}

func (t *TxRelayerImpl) sendTransactionLocked(txn *ethgo.Transaction, key ethgo.Key) (ethgo.Hash, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	nonce, err := t.getNextNonce(key.Address())
	if err != nil {
		return ethgo.ZeroHash, err
	}
	fmt.Println("[txRelayer] found nonce", nonce)

	txn.Nonce = nonce

	txn.From = key.Address()

	if txn.GasPrice == 0 {
		gasPrice, err := t.Client().Eth().GasPrice()
		if err != nil {
			return ethgo.ZeroHash, err
		}

		txn.GasPrice = gasPrice + (gasPrice * gasPricePercent / 100)
	}

	if txn.Gas == 0 {
		gasLimit, err := t.client.Eth().EstimateGas(ConvertTxnToCallMsg(txn))
		if err != nil {
			return ethgo.ZeroHash, err
		}

		txn.Gas = gasLimit + (gasLimit * gasLimitPercent / 100)
	}

	chainID, err := t.client.Eth().ChainID()
	if err != nil {
		return ethgo.ZeroHash, err
	}

	signer := wallet.NewEIP155Signer(chainID.Uint64())
	if txn, err = signer.SignTx(txn, key); err != nil {
		return ethgo.ZeroHash, err
	}

	data, err := txn.MarshalRLPTo(nil)
	if err != nil {
		return ethgo.ZeroHash, err
	}

	if t.writer != nil {
		_, _ = t.writer.Write([]byte(
			fmt.Sprintf("[TxRelayer.SendTransaction]\nFrom = %s \nGas = %d \nGas Price = %d\nNonce=%v\n",
				txn.From, txn.Gas, txn.GasPrice, txn.Nonce)))
	}

	return t.client.Eth().SendRawTransaction(data)
}

// SendTransactionLocal sends non-signed transaction
// (this function is meant only for testing purposes and is about to be removed at some point)
func (t *TxRelayerImpl) SendTransactionLocal(txn *ethgo.Transaction) (*ethgo.Receipt, error) {
	accounts, err := t.client.Eth().Accounts()
	if err != nil {
		return nil, err
	}

	if len(accounts) == 0 {
		return nil, errNoAccounts
	}

	txn.From = accounts[0]

	gasLimit, err := t.client.Eth().EstimateGas(ConvertTxnToCallMsg(txn))
	if err != nil {
		return nil, err
	}

	txn.Gas = gasLimit
	txn.GasPrice = defaultGasPrice

	txnHash, err := t.client.Eth().SendTransaction(txn)
	if err != nil {
		return nil, err
	}

	return t.waitForReceipt(txnHash)
}

func (t *TxRelayerImpl) waitForReceipt(hash ethgo.Hash) (*ethgo.Receipt, error) {
	count := uint(0)

	for {
		receipt, err := t.client.Eth().GetTransactionReceipt(hash)
		if err != nil {
			if err.Error() != "not found" {
				return nil, err
			}
		}

		if receipt != nil {
			return receipt, nil
		}

		if count > numRetries {
			return nil, fmt.Errorf("timeout while waiting for transaction %s to be processed", hash)
		}

		time.Sleep(t.receiptTimeout)
		count++
	}
}

func (t *TxRelayerImpl) getNextNonce(address ethgo.Address) (uint64, error) {
	t.nonceMapMtx.Lock()
	defer t.nonceMapMtx.Unlock()

	if !t.useNonceMap {
		fmt.Println("[txRelayer] getting nonce from pending")
		nonce, err := t.client.Eth().GetNonce(address, ethgo.Pending)
		if err != nil {
			return 0, err
		}
		return nonce, nil
	}

	fmt.Println("[txRelayer] getting nonce with nonce map")
	nonce, found := t.nonceMap[address]

	if found {
		nonce++
	} else {
		var err error
		nonce, err = t.client.Eth().GetNonce(address, ethgo.Pending)
		if err != nil {
			return 0, err
		}
	}

	if t.writer != nil {
		_, _ = t.writer.Write([]byte(
			fmt.Sprintf("[TxRelayer.getNextNonce]\nAddress = %s \nNonce = %v\n",
				address, nonce)))
	}

	t.nonceMap[address] = nonce

	return nonce, nil
}

// ConvertTxnToCallMsg converts txn instance to call message
func ConvertTxnToCallMsg(txn *ethgo.Transaction) *ethgo.CallMsg {
	return &ethgo.CallMsg{
		From:     txn.From,
		To:       txn.To,
		Data:     txn.Input,
		GasPrice: txn.GasPrice,
		Value:    txn.Value,
		Gas:      new(big.Int).SetUint64(txn.Gas),
	}
}

type TxRelayerOption func(*TxRelayerImpl)

func EnableNonceMap() TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.useNonceMap = true
	}
}

func WithClient(client *jsonrpc.Client) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.client = client
	}
}

func WithIPAddress(ipAddress string) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.ipAddress = ipAddress
	}
}

func WithReceiptTimeout(receiptTimeout time.Duration) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.receiptTimeout = receiptTimeout
	}
}

func WithWriter(writer io.Writer) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.writer = writer
	}
}
