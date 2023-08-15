package signer

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/umbracle/fastrlp"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
)

func JSONMarshalHelper(t *testing.T, extra *IstanbulExtra) string {
	t.Helper()

	res, err := json.Marshal(extra)

	assert.NoError(t, err)

	return string(res)
}

func TestIstanbulExtraMarshalAndUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		extra *IstanbulExtra
	}{
		{
			name: "ECDSAExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					validators.NewECDSAValidator(
						testAddr1,
					),
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
				ParentCommittedSeals: &SerializedSeal{
					[]byte{0x3},
					[]byte{0x4},
				},
			},
		},
		{
			name: "ECDSAExtra without ParentCommittedSeals",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
			},
		},
		{
			name: "BLSExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
		},
		{
			name: "BLSExtra without ParentCommittedSeals",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// create original data
			originalExtraJSON := JSONMarshalHelper(t, test.extra)

			bytesData := test.extra.MarshalRLPTo(nil, EncodeEverything)
			err := test.extra.UnmarshalRLP(bytesData)
			assert.NoError(t, err)

			// make sure all data is recovered
			assert.Equal(
				t,
				originalExtraJSON,
				JSONMarshalHelper(t, test.extra),
			)
		})
	}
}

func Test_packProposerSealIntoExtra(t *testing.T) {
	t.Parallel()

	newProposerSeal := []byte("new proposer seal")

	tests := []struct {
		name  string
		extra *IstanbulExtra
	}{
		{
			name: "ECDSAExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
				ParentCommittedSeals: &SerializedSeal{
					[]byte{0x3},
					[]byte{0x4},
				},
			},
		},
		{
			name: "ECDSAExtra without ParentCommittedSeals",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
			},
		},
		{
			name: "BLSExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
		},
		{
			name: "BLSExtra without ParentCommittedSeals",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			originalProposerSeal := test.extra.ProposerSeal

			// create expected data
			test.extra.ProposerSeal = newProposerSeal
			expectedJSON := JSONMarshalHelper(t, test.extra)
			test.extra.ProposerSeal = originalProposerSeal

			newExtraBytes := packProposerSealIntoExtra(
				// prepend IstanbulExtraHeader to parse
				append(
					make([]byte, IstanbulExtraVanity),
					test.extra.MarshalRLPTo(nil, EncodeEverything)...,
				),
				newProposerSeal,
			)

			assert.NoError(
				t,
				test.extra.UnmarshalRLP(newExtraBytes[IstanbulExtraVanity:]),
			)

			// check json of decoded data matches with the original data
			jsonData := JSONMarshalHelper(t, test.extra)

			assert.Equal(
				t,
				expectedJSON,
				jsonData,
			)
		})
	}
}

func Test_packCommittedSealsAndRoundNumberIntoExtra(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		extra             *IstanbulExtra
		newCommittedSeals Seals
		roundNumber       *uint64
	}{
		{
			name: "ECDSAExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
				ParentCommittedSeals: &SerializedSeal{
					[]byte{0x3},
					[]byte{0x4},
				},
			},
			newCommittedSeals: &SerializedSeal{
				[]byte{0x3},
				[]byte{0x4},
			},
			roundNumber: nil,
		},
		{
			name: "ECDSAExtra without ParentCommittedSeals",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
			},
			newCommittedSeals: &SerializedSeal{
				[]byte{0x3},
				[]byte{0x4},
			},
			roundNumber: nil,
		},
		{
			name: "BLSExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
			newCommittedSeals: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBytes([]byte{0xa}),
				Signature: []byte{0x2},
			},
			roundNumber: nil,
		},
		{
			name: "BLSExtra without ParentCommittedSeals",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
			newCommittedSeals: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBytes([]byte{0xa}),
				Signature: []byte{0x2},
			},
			roundNumber: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			originalCommittedSeals := test.extra.CommittedSeals

			// create expected data
			test.extra.CommittedSeals = test.newCommittedSeals
			expectedJSON := JSONMarshalHelper(t, test.extra)
			test.extra.CommittedSeals = originalCommittedSeals

			// update committed seals
			newExtraBytes := packCommittedSealsAndRoundNumberIntoExtra(
				// prepend IstanbulExtraHeader
				append(
					make([]byte, IstanbulExtraVanity),
					test.extra.MarshalRLPTo(nil, EncodeEverything)...,
				),
				test.newCommittedSeals,
				test.roundNumber,
			)

			// decode RLP data
			assert.NoError(
				t,
				test.extra.UnmarshalRLP(newExtraBytes[IstanbulExtraVanity:]),
			)

			// check json of decoded data matches with the original data
			jsonData := JSONMarshalHelper(t, test.extra)

			assert.Equal(
				t,
				expectedJSON,
				jsonData,
			)
		})
	}
}

func Test_unmarshalRLPForParentCS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		extra       *IstanbulExtra
		targetExtra *IstanbulExtra
	}{
		{
			name: "ECDSAExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
				ParentCommittedSeals: &SerializedSeal{
					[]byte{0x3},
					[]byte{0x4},
				},
			},
			targetExtra: &IstanbulExtra{
				ParentCommittedSeals: &SerializedSeal{},
			},
		},
		{
			name: "BLSExtra",
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
			targetExtra: &IstanbulExtra{
				ParentCommittedSeals: &AggregatedSeal{},
			},
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			bytesData := test.extra.MarshalRLPTo(nil, EncodeEverything)

			assert.NoError(t, test.targetExtra.unmarshalRLPForParentCS(bytesData))

			// make sure all data is recovered
			assert.Equal(
				t,
				test.extra.ParentCommittedSeals,
				test.targetExtra.ParentCommittedSeals,
			)
		})
	}
}

func Test_putIbftExtra(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header *types.Header
		extra  *IstanbulExtra
	}{
		{
			name: "ECDSAExtra",
			header: &types.Header{
				ExtraData: []byte{},
			},
			extra: &IstanbulExtra{
				Validators: validators.NewECDSAValidatorSet(
					ecdsaValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &SerializedSeal{
					[]byte{0x1},
					[]byte{0x2},
				},
				ParentCommittedSeals: &SerializedSeal{
					[]byte{0x3},
					[]byte{0x4},
				},
			},
		},
		{
			name: "BLSExtra",
			header: &types.Header{
				ExtraData: make([]byte, IstanbulExtraVanity+10),
			},
			extra: &IstanbulExtra{
				Validators: validators.NewBLSValidatorSet(
					blsValidator1,
				),
				ProposerSeal: testProposerSeal,
				CommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x8}),
					Signature: []byte{0x1},
				},
				ParentCommittedSeals: &AggregatedSeal{
					Bitmap:    new(big.Int).SetBytes([]byte{0x9}),
					Signature: []byte{0x2},
				},
			},
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			putIbftExtra(test.header, test.extra, EncodeEverything)

			expectedExtraHeader := make([]byte, IstanbulExtraVanity)
			expectedExtraBody := test.extra.MarshalRLPTo(nil, EncodeEverything)
			expectedExtra := append(expectedExtraHeader, expectedExtraBody...) //nolint:makezero

			assert.Equal(
				t,
				expectedExtra,
				test.header.ExtraData,
			)
		})
	}
}

func Test_HexUnmarshallsForPalm(t *testing.T) {
	rlpHex := "f87ea00000000000000000000000000000000000000000000000000000000000000000f8549445c09591de259c4a81cdec4779cb50019933ca30941cf698ebfdc7a528ca02760c4d49d1bb0423486c9448d4295c257384b3264a4572f3a1268a45b7095f948ddf64b1e33ae6f333f68c96f5712d327f4cb075808400000000c0"
	bytes, err := hex.DecodeString(rlpHex)
	if err != nil {
		t.Fatal(err)
	}

	pr := fastrlp.DefaultParserPool.Get()

	v, err := pr.Parse(bytes)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, 5, v.Elems())
}

func Test_PalmExtraMarshallsToHex(t *testing.T) {
	rlpHex := "f87ea00000000000000000000000000000000000000000000000000000000000000000f8549445c09591de259c4a81cdec4779cb50019933ca30941cf698ebfdc7a528ca02760c4d49d1bb0423486c9448d4295c257384b3264a4572f3a1268a45b7095f948ddf64b1e33ae6f333f68c96f5712d327f4cb075808400000000c0"
	bytes, err := hex.DecodeString(rlpHex)
	if err != nil {
		t.Fatal(err)
	}

	// now remove the validators and add a single one back
	extra := IstanbulExtra{
		isPalm:         true,
		Validators:     validators.NewECDSAValidatorSet(),
		Vote:           Vote{},
		CommittedSeals: &SecpSeals{},
	}
	err = extra.UnmarshalRLP(bytes)
	if err != nil {
		t.Fatal(err)
	}

	newValidator1 := validators.NewECDSAValidator(types.StringToAddress("0x6075ad7bdb9a14e51bad64044342db8682f6ef18"))
	newValidator2 := validators.NewECDSAValidator(types.StringToAddress("0x9dd45a8acc3d1da8dfb6cda9f3d2110eff780ccf"))
	newValidator3 := validators.NewECDSAValidator(types.StringToAddress("0xd3cb39e9e2fb57fb16efa4dc96dce66e2ce27221"))
	extra.Validators = validators.NewECDSAValidatorSet()
	err = extra.Validators.Add(newValidator1)
	err = extra.Validators.Add(newValidator2)
	err = extra.Validators.Add(newValidator3)
	if err != nil {
		t.Fatal(err)
	}

	reMarshalled := extra.MarshalRLPTo([]byte{}, EncodeEverything)
	reHex := hex.EncodeToString(reMarshalled)

	expected := "f869a00000000000000000000000000000000000000000000000000000000000000000f83f946075ad7bdb9a14e51bad64044342db8682f6ef18949dd45a8acc3d1da8dfb6cda9f3d2110eff780ccf94d3cb39e9e2fb57fb16efa4dc96dce66e2ce27221808400000000c0"
	assert.Equal(t, expected, reHex)
}
