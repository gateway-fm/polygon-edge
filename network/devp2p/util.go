package devp2p

import (
	"github.com/ethereum/go-ethereum/common"
	gethTypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/0xPolygon/polygon-edge/types"
)

func GethHashConvert(in common.Hash) types.Hash {
	return types.BytesToHash(in[:])
}

func EdgeHashToGethHash(in types.Hash) common.Hash {
	return common.BytesToHash(in.Bytes())
}

func GethAddressConvert(addr common.Address) []byte {
	return addr.Bytes()
}

func GethAddressToAddresConvert(addr *common.Address) *types.Address {
	if addr == nil {
		return nil
	}

	res := types.BytesToAddress(addr.Bytes())
	return &res
}

func GethLogsBloomConvert(in gethTypes.Bloom) types.Bloom {
	var bloom types.Bloom
	bloom.SetBytes(in.Bytes())
	return bloom
}

func GethNonceConvert(in gethTypes.BlockNonce) types.Nonce {
	return types.EncodeNonce(in.Uint64())
}

func GethTxTypeConvert(in uint8) types.TxType {
	return types.TxType(in)
}

func GethHeaderConvertNoHash(h *gethTypes.Header) *types.Header {
	var baseFee uint64 = 0
	if h.BaseFee != nil {
		baseFee = h.BaseFee.Uint64()
	}

	return &types.Header{
		ParentHash:   GethHashConvert(h.ParentHash),
		Sha3Uncles:   GethHashConvert(h.UncleHash),
		Miner:        GethAddressConvert(h.Coinbase),
		StateRoot:    GethHashConvert(h.Root),
		TxRoot:       GethHashConvert(h.TxHash),
		ReceiptsRoot: GethHashConvert(h.ReceiptHash),
		LogsBloom:    GethLogsBloomConvert(h.Bloom),
		Difficulty:   h.Difficulty.Uint64(),
		Number:       h.Number.Uint64(),
		GasLimit:     h.GasLimit,
		GasUsed:      h.GasUsed,
		Timestamp:    h.Time,
		ExtraData:    h.Extra,
		MixHash:      GethHashConvert(h.MixDigest),
		Nonce:        GethNonceConvert(h.Nonce),
		Hash:         GethHashConvert(h.Hash()),
		BaseFee:      baseFee,
	}
}

func GethHeaderConvert(h *gethTypes.Header) (*types.Header, error) {
	return GethHeaderConvertNoHash(h), nil
}

func GethTransactionConvert(t *gethTypes.Transaction) *types.Transaction {
	v, r, s := t.RawSignatureValues()
	result := &types.Transaction{
		Nonce:     t.Nonce(),
		GasPrice:  t.GasPrice(),
		GasTipCap: t.GasTipCap(),
		GasFeeCap: t.GasFeeCap(),
		Gas:       t.Gas(),
		To:        GethAddressToAddresConvert(t.To()),
		Value:     t.Value(),
		Input:     t.Data(),
		V:         v,
		R:         r,
		S:         s,
		Hash:      GethHashConvert(t.Hash()),
		Type:      GethTxTypeConvert(t.Type()),
		ChainID:   t.ChainId(),
	}

	for _, at := range t.AccessList() {
		tup := types.AccessTuple{
			Address: GethAddressToAddresConvert(&at.Address),
		}

		for _, sk := range at.StorageKeys {
			tup.StorageKeys = append(tup.StorageKeys, GethHashConvert(sk))
		}

		result.AccessList = append(result.AccessList, tup)
	}

	return result
}
