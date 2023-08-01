package consensus

import (
	"github.com/0xPolygon/polygon-edge/types"
)

var DefaultHashStore = &HashFunctions{
	Hashers: make([]Hasher, 0),
}

type HashFunctions struct {
	Hashers []Hasher
}

type Hasher struct {
	Hash    func(h *types.Header) types.Hash
	ToBlock *uint64
}

func (h *HashFunctions) RegisterNewHasher(f func(h *types.Header) types.Hash, toBlock *uint64) {
	h.Hashers = append(h.Hashers, Hasher{Hash: f, ToBlock: toBlock})
}

func (h *HashFunctions) RegisterSelfAsHandler() {
	types.HeaderHash = h.CalculateHash
}

func (h *HashFunctions) CalculateHash(head *types.Header) types.Hash {
	// loop over the hashers we have in order of registration
	for _, hasher := range h.Hashers {
		if hasher.ToBlock == nil || *hasher.ToBlock >= head.Number {
			return hasher.Hash(head)
		}
	}
	return types.ZeroHash
}
