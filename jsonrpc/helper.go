package jsonrpc

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/types"
)

var (
	ErrHeaderNotFound           = errors.New("header not found")
	ErrLatestNotFound           = errors.New("latest header not found")
	ErrNegativeBlockNumber      = errors.New("invalid argument 0: block number must not be negative")
	ErrFailedFetchGenesis       = errors.New("error fetching genesis block header")
	ErrNoDataInContractCreation = errors.New("contract creation without data provided")
)

type latestHeaderGetter interface {
	Header() *types.Header
}

// GetNumericBlockNumber returns block number based on current state or specified number
func GetNumericBlockNumber(number BlockNumber, container *StoreContainer) (uint64, JSONRPCStore, error) {
	switch number {
	case LatestBlockNumber, PendingBlockNumber:
		store := container.latest()
		latest := store.Header()
		if latest == nil {
			return 0, nil, ErrLatestNotFound
		}

		return latest.Number, store, nil

	case EarliestBlockNumber:
		return 0, container.earliest(), nil

	default:
		if number < 0 {
			return 0, nil, ErrNegativeBlockNumber
		}

		store := container.byNumber(number)
		return uint64(number), store, nil
	}
}

type headerGetter interface {
	Header() *types.Header
	GetHeaderByNumber(uint64) (*types.Header, bool)
}

// GetBlockHeader returns a header using the provided number
func GetBlockHeader(number BlockNumber, container *StoreContainer) (*types.Header, JSONRPCStore, error) {
	switch number {
	case PendingBlockNumber, LatestBlockNumber:
		store := container.latest()
		return store.Header(), store, nil

	case EarliestBlockNumber:
		store := container.earliest()
		header, ok := store.GetHeaderByNumber(uint64(0))
		if !ok {
			return nil, nil, ErrFailedFetchGenesis
		}

		return header, store, nil

	default:
		store := container.byNumber(number)
		// Convert the block number from hex to uint64
		header, ok := store.GetHeaderByNumber(uint64(number))
		if !ok {
			return nil, nil, fmt.Errorf("error fetching block number %d header", uint64(number))
		}

		return header, store, nil
	}
}

type txLookupAndBlockGetter interface {
	ReadTxLookup(types.Hash) (types.Hash, bool)
	GetBlockByHash(types.Hash, bool) (*types.Block, bool)
}

// GetTxAndBlockByTxHash returns the tx and the block including the tx by given tx hash
func GetTxAndBlockByTxHash(txHash types.Hash, container *StoreContainer) (*types.Transaction, *types.Block, JSONRPCStore, error) {
	// get the latest store as it will have access to all world state
	latestStore := container.latest()

	blockHash, ok := latestStore.ReadTxLookup(txHash)
	if !ok {
		return nil, nil, nil, fmt.Errorf("tx %s not found", txHash.String())
	}

	// now we can get the actual block by hash using the container
	store, b, err := container.byHash(blockHash, true)
	if err != nil {
		return nil, nil, nil, err
	}

	if txn, _ := types.FindTxByHash(b.Transactions, txHash); txn != nil {
		return txn, b, store, nil
	}

	return nil, nil, nil, fmt.Errorf("tx %s not found in block", txHash.String())
}

type blockGetter interface {
	Header() *types.Header
	GetHeaderByNumber(uint64) (*types.Header, bool)
	GetBlockByHash(types.Hash, bool) (*types.Block, bool)
}

func GetHeaderFromBlockNumberOrHash(bnh BlockNumberOrHash, container *StoreContainer) (*types.Header, JSONRPCStore, error) {
	// The filter is empty, use the latest block by default
	if bnh.BlockNumber == nil && bnh.BlockHash == nil {
		bnh.BlockNumber, _ = createBlockNumberPointer(latest)
	}

	if bnh.BlockNumber != nil {
		// block number
		header, store, err := GetBlockHeader(*bnh.BlockNumber, container)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get the header of block %d: %w", *bnh.BlockNumber, err)
		}

		return header, store, nil
	}

	// block hash
	store, b, err := container.byHash(*bnh.BlockHash, false)
	if err != nil {
		return nil, nil, fmt.Errorf("could not find block referenced by the hash %s, err %w", bnh.BlockHash.String(), err)
	}

	return b.Header, store, nil
}

type nonceGetter interface {
	Header() *types.Header
	GetHeaderByNumber(uint64) (*types.Header, bool)
	GetNonce(types.Address) uint64
	GetAccount(root types.Hash, addr types.Address) (*Account, error)
}

func GetNextNonce(address types.Address, number BlockNumber, container *StoreContainer) (uint64, error) {
	if number == PendingBlockNumber {
		// Grab the latest pending nonce from the TxPool
		//
		// If the account is not initialized in the local TxPool,
		// return the latest nonce from the world state
		store := container.latest()
		res := store.GetNonce(address)

		return res, nil
	}

	header, store, err := GetBlockHeader(number, container)
	if err != nil {
		return 0, err
	}

	acc, err := store.GetAccount(header.StateRoot, address)

	//nolint:govet
	if errors.Is(err, ErrStateNotFound) {
		// If the account doesn't exist / isn't initialized,
		// return a nonce value of 0
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	return acc.Nonce, nil
}

func DecodeTxn(arg *txnArgs, blockNumber uint64, container *StoreContainer) (*types.Transaction, error) {
	if arg == nil {
		return nil, errors.New("missing value for required argument 0")
	}
	// set default values
	if arg.From == nil {
		arg.From = &types.ZeroAddress
		arg.Nonce = argUintPtr(0)
	} else if arg.Nonce == nil {
		// get nonce from the pool
		nonce, err := GetNextNonce(*arg.From, LatestBlockNumber, container)
		if err != nil {
			return nil, err
		}
		arg.Nonce = argUintPtr(nonce)
	}

	if arg.Value == nil {
		arg.Value = argBytesPtr([]byte{})
	}

	if arg.GasPrice == nil {
		arg.GasPrice = argBytesPtr([]byte{})
	}

	if arg.GasTipCap == nil {
		arg.GasTipCap = argBytesPtr([]byte{})
	}

	if arg.GasFeeCap == nil {
		arg.GasFeeCap = argBytesPtr([]byte{})
	}

	var input []byte
	if arg.Data != nil {
		input = *arg.Data
	} else if arg.Input != nil {
		input = *arg.Input
	}

	if arg.To == nil && input == nil {
		return nil, ErrNoDataInContractCreation
	}

	if input == nil {
		input = []byte{}
	}

	if arg.Gas == nil {
		arg.Gas = argUintPtr(0)
	}

	txType := types.LegacyTx
	if arg.Type != nil {
		txType = types.TxType(*arg.Type)
	}

	txn := &types.Transaction{
		From:      *arg.From,
		Gas:       uint64(*arg.Gas),
		GasPrice:  new(big.Int).SetBytes(*arg.GasPrice),
		GasTipCap: new(big.Int).SetBytes(*arg.GasTipCap),
		GasFeeCap: new(big.Int).SetBytes(*arg.GasFeeCap),
		Value:     new(big.Int).SetBytes(*arg.Value),
		Input:     input,
		Nonce:     uint64(*arg.Nonce),
		Type:      txType,
	}

	if arg.To != nil {
		txn.To = arg.To
	}

	txn.ComputeHash(blockNumber)

	return txn, nil
}
