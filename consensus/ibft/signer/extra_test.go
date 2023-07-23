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
	rlpHex := "f901b6a00000000000000000000000000000000000000000000000000000000000000000f87e9411781ba3cd85671a6f8481514f84bce660b75919944324df543421b7b3dc29a59fcd2416aa9a4f4717947f9a67f84a010bda3d83493e4f1476f2651b1dab9488cd6a0d883f9104432d729df772131efe44b82094948b655e3a1e3505c57d15f2c5c813e4abad9cb494b49ce87bcb7f8a1dde59bde1b4c18fbf00b424ac808400000000f9010cb8412d7b64bec5c8a17ec70a878625ca37676733cd3ac65a37118571e7df7c4119ca3245537ec3082f46ce04621b2c8c76b4fff6400db3377f65bd7529adae55e89201b841619764d2c72b75c054eba9beb351649633cf3306e39d94c36ca4c2a1de862ba76466b3f06a6fdfde842db54042dca4c4829d5e3537dbb0070f653dd1b3e7e30501b841f310494c49981c2249fdbb8f18ea680f9c74737c64fc663936a26105a9f2cc25567d2f42478c308ded9405f6253ccb894a037afa41e8209c5b920e2245a7f40101b841347b7b0a0bad02f72bcfcec9d4d3b552d326a6c111f8040082ea6297f256143c37b06d4e51be1780e3134c4cdcc374abf3018840ff6ee8c0ba08d5f8886d39ab00"
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
