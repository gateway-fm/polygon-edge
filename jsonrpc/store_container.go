package jsonrpc

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/types"
)

// StoreContainer holds on to different JSONRPCStore implementations.  Designed to be used to suppport
// forks across consensus engines.  The jsonrpc instance making use of this should be stopped when
// manipulating the stores slice.  We don't want to use mutexes for the majority of the time as it
// hamper performance for hash based calls whilst they go to the db
type StoreContainer struct {
	stores []ForkedStore
	db     storage.Storage
}

type ForkedStore struct {
	To    *uint64
	Store JSONRPCStore
}

func NewStoreContainer(db storage.Storage) *StoreContainer {
	return &StoreContainer{
		stores: make([]ForkedStore, 0),
		db:     db,
	}
}

func (s *StoreContainer) AddStore(store JSONRPCStore, to *uint64) {
	s.stores = append(s.stores, ForkedStore{To: to, Store: store})
}

func (s *StoreContainer) Reset() {
	s.stores = make([]ForkedStore, 0)
}

func (s *StoreContainer) byNumber(blockNumber BlockNumber) JSONRPCStore {
	// first check for the special numbers
	switch blockNumber {
	case LatestBlockNumber, PendingBlockNumber:
		return s.latest()
	case EarliestBlockNumber:
		return s.earliest()
	}

	// check all stores that do not have a nil to field first
	for _, store := range s.stores {
		if store.To == nil {
			continue
		}
		if uint64(blockNumber) < *store.To {
			return store.Store
		}
	}

	// if we haven't found one then return the first with a nil to field
	for _, store := range s.stores {
		if store.To == nil {
			return store.Store
		}
	}

	// if we haven't found one then return the last one added
	return s.stores[len(s.stores)-1].Store
}

// getByHash will optimise by using the latest store and working backwards until it finds the block
// or finds nothing in which case it will return an error
func (s *StoreContainer) byHash(hash types.Hash, full bool) (JSONRPCStore, *types.Block, error) {
	for i := len(s.stores) - 1; i >= 0; i-- {
		store := s.stores[i]
		b, found := store.Store.GetBlockByHash(hash, full)
		if !found {
			continue
		}
		return store.Store, b, nil
	}

	return nil, nil, fmt.Errorf("block %s not found", hash.String())
}

func (s *StoreContainer) byFilter(filter BlockNumberOrHash) (JSONRPCStore, error) {
	if filter.BlockNumber != nil {
		switch *filter.BlockNumber {
		case LatestBlockNumber, PendingBlockNumber:
			return s.latest(), nil
		case EarliestBlockNumber:
			return s.earliest(), nil
		default:
			num := *filter.BlockNumber
			return s.byNumber(num), nil
		}
	}

	store, _, err := s.byHash(*filter.BlockHash, false)
	return store, err
}

func (s *StoreContainer) latest() JSONRPCStore {
	return s.stores[len(s.stores)-1].Store
}

func (s *StoreContainer) earliest() JSONRPCStore {
	return s.stores[0].Store
}
