package types

import (
	"github.com/umbracle/fastrlp"

	"github.com/0xPolygon/polygon-edge/helper/keccak"
)

var HeaderHash func(h *Header) Hash
var HeaderHashWithoutSeals func(h *Header) Hash

// This is the default header hash for the block.
// In IBFT, this header hash method is substituted
// for Istanbul Header Hash calculation
func init() {
	ResetHeaderHash()
}

var marshalArenaPool fastrlp.ArenaPool

func DefaultHeaderHash(h *Header) (hash Hash) {
	// default header hashing
	ar := marshalArenaPool.Get()
	hasher := keccak.DefaultKeccakPool.Get()

	v := h.MarshalRLPWith(ar)
	hasher.WriteRlp(hash[:0], v)

	marshalArenaPool.Put(ar)
	keccak.DefaultKeccakPool.Put(hasher)

	return
}

func ResetHeaderHash() {
	HeaderHash = DefaultHeaderHash
}

// ComputeHash computes the hash of the header
func (h *Header) ComputeHash() *Header {
	h.Hash = HeaderHash(h)

	return h
}

func (h *Header) ComputeHashWithoutSeals() Hash {
	return HeaderHashWithoutSeals(h)
}
