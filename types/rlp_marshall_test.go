package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/umbracle/fastrlp"

	"github.com/0xPolygon/polygon-edge/helper/hex"
)

var signerPool fastrlp.ArenaPool

func Test_RlpMarshallAccessList_EncodesAndDecodesAsExpected(t *testing.T) {
	addr1 := BytesToAddress([]byte{0x01})
	addr2 := BytesToAddress([]byte{0x02})

	tuple := []AccessTuple{
		{
			Address:     &addr1,
			StorageKeys: []Hash{{0x01}},
		},
		{
			Address:     &addr2,
			StorageKeys: []Hash{{0x02}},
		},
	}

	a := signerPool.Get()
	vv := a.NewArray()

	RlpEncodeAccessList(a, vv, tuple)

	element := vv.Get(0)
	decoded, err := RlpDecodeAccessList(element)
	assert.NoError(t, err)
	assert.Equal(t, tuple, decoded)
}

func Test_RlpUnmarshallRawTx_HandlesAccessList(t *testing.T) {
	rawRlp := "0x01f9014e8502a15c308354825208830493e094f6896cd6202521681970aa31207fa4eaf33dc5e780b8e46b20c454000000000000000000000000fd4419c7bb7e41dd42edbbc184033858801005df000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000000000000000000a0000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000c30ef402e231a36079f030773a62960600000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000000000000000000000001c080a01274cc41bc535b8ec5f42acfb7ae6a719e5fece72102a84b5d56bee07fc4e67aa0034ff85afac5d8095c50fc2515ff403508f0905995707b852a2602401cd20975"

	decoded, err := hex.DecodeHex(rawRlp)
	if err != nil {
		t.Fatal(err)
	}

	tr := &Transaction{}
	err = tr.UnmarshalRLP(decoded)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, AccessListTx, int(tr.Type))
	assert.Equal(t, 0, len(tr.AccessList))

	addr1 := BytesToAddress([]byte{0x01})
	addr2 := BytesToAddress([]byte{0x02})
	tr.AccessList = []AccessTuple{
		{
			Address:     &addr1,
			StorageKeys: []Hash{{0x01}, {0x02}},
		},
		{
			Address:     &addr2,
			StorageKeys: []Hash{{0x03}, {0x04}},
		},
	}

	encoded := tr.MarshalRLP()
	if err != nil {
		t.Fatal(err)
	}

	tr2 := &Transaction{}
	err = tr2.UnmarshalRLP(encoded)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, 2, len(tr2.AccessList))
	assert.Equal(t, tr2.AccessList[0].Address, &addr1)
	assert.Equal(t, tr2.AccessList[0].StorageKeys[0], Hash{0x01})
	assert.Equal(t, tr2.AccessList[0].StorageKeys[1], Hash{0x02})
	assert.Equal(t, tr2.AccessList[1].Address, &addr2)
	assert.Equal(t, tr2.AccessList[1].StorageKeys[0], Hash{0x03})
	assert.Equal(t, tr2.AccessList[1].StorageKeys[1], Hash{0x04})

	// now dump the access list and re-code/de-code
	tr2.AccessList = []AccessTuple{}
	encoded = tr2.MarshalRLP()

	tr3 := &Transaction{}
	err = tr3.UnmarshalRLP(encoded)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, 0, len(tr3.AccessList))

}
