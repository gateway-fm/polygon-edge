package buildroot

import (
	"github.com/umbracle/fastrlp"

	"github.com/0xPolygon/polygon-edge/helper/keccak"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
)

var arenaPool fastrlp.ArenaPool

// CalculateReceiptsRoot calculates the root of a list of receipts
func CalculateReceiptsRoot(receipts []*types.Receipt) types.Hash {
	ar := arenaPool.Get()

	res := calculateRootWithRlp(len(receipts), func(i int) (*fastrlp.Value, types.TxType) {
		ar.Reset()

		r := receipts[i]

		return r.MarshalRLPWith(ar), r.TransactionType
	})

	arenaPool.Put(ar)

	return res
}

// CalculateTransactionsRoot calculates the root of a list of transactions
func CalculateTransactionsRoot(transactions []*types.Transaction, blockNumber uint64) types.Hash {
	handler := types.GetTransactionHashHandler(blockNumber)

	return CalculateRoot(len(transactions), func(indx int) []byte {
		return handler.SerializeForRootCalculation(transactions[indx], &arenaPool)
	})
}

// CalculateUncleRoot calculates the root of a list of uncles
func CalculateUncleRoot(uncles []*types.Header) types.Hash {
	if len(uncles) == 0 {
		return types.EmptyUncleHash
	}

	a := arenaPool.Get()
	v := a.NewArray()

	for _, i := range uncles {
		v.Set(i.MarshalRLPWith(a))
	}

	root := keccak.Keccak256Rlp(nil, v)

	arenaPool.Put(a)

	return types.BytesToHash(root)
}

func calculateRootWithRlp(num int, h func(indx int) (val *fastrlp.Value, typ types.TxType)) types.Hash {
	hF := func(indx int) []byte {
		val, typ := h(indx)
		b := val.MarshalTo(nil)

		// we need to prepend the transaction type if it is not a legacy transaction
		if typ != types.LegacyTx {
			b = append([]byte{byte(typ)}, b...)
		}

		return b
	}

	return CalculateRoot(num, hF)
}

// CalculateRoot calculates a root with a callback
func CalculateRoot(num int, h func(indx int) []byte) types.Hash {
	if num == 0 {
		return types.EmptyRootHash
	}

	if num <= 128 {
		fastH := acquireFastHasher()
		dst, ok := fastH.Hash(num, h)

		// important to copy the return before releasing the hasher
		res := types.BytesToHash(dst)

		releaseFastHasher(fastH)

		if ok {
			return res
		}
	}

	// fallback to slow hash
	return types.BytesToHash(deriveSlow(num, h))
}

var numArenaPool fastrlp.ArenaPool

func deriveSlow(num int, h func(indx int) []byte) []byte {
	t := itrie.NewTrie()
	txn := t.Txn(nil)

	ar := numArenaPool.Get()
	for i := 0; i < num; i++ {
		indx := ar.NewUint(uint64(i))
		txn.Insert(indx.MarshalTo(nil), h(i))
		ar.Reset()
	}

	numArenaPool.Put(ar)

	x, _ := txn.Hash()

	return x
}
