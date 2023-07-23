package signer

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/umbracle/fastrlp"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
)

var (
	// IstanbulDigest represents a hash of "Istanbul practical byzantine fault tolerance"
	// to identify whether the block is from Istanbul consensus engine
	IstanbulDigest = types.StringToHash("0x63746963616c2062797a616e74696e65206661756c7420746f6c6572616e6365")

	// IstanbulExtraVanity represents a fixed number of extra-data bytes reserved for proposer vanity
	IstanbulExtraVanity = 32

	// IstanbulExtraSeal represents the fixed number of extra-data bytes reserved for proposer seal
	IstanbulExtraSeal = 65

	zeroBytes = make([]byte, 32)

	errRoundNumberOverflow = errors.New("round number is out of range for 64bit")

	emptyVanity = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
)

type EncodingMode int

const (
	EncodeEverything           EncodingMode = iota
	ExcludeRoundAndCommitSeals EncodingMode = iota
	ExcludeCommitSeals         EncodingMode = iota
)

// IstanbulExtra defines the structure of the extra field for Istanbul
type IstanbulExtra struct {
	Validators           validators.Validators
	ProposerSeal         []byte
	CommittedSeals       Seals
	ParentCommittedSeals Seals
	RoundNumber          *uint64
	Vanity               []byte
	Vote                 Vote

	isPalm bool // used to alter code flow when operating in the Palm network
}

const addVote = 0xFF
const dropVote = 0x0

type PalmIstanbulExtra struct {
	*IstanbulExtra
}

type Seals interface {
	// Number of committed seals
	Num() int
	MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value
	UnmarshalRLPFrom(*fastrlp.Parser, *fastrlp.Value) error
}

// parseRound parses RLP-encoded bytes into round
func parseRound(v *fastrlp.Value) (*uint64, error) {
	roundBytes, err := v.Bytes()
	if err != nil {
		return nil, err
	}

	if len(roundBytes) > 8 {
		return nil, errRoundNumberOverflow
	}

	if len(roundBytes) == 0 {
		return nil, nil
	}

	round := binary.BigEndian.Uint32(roundBytes)

	as64 := uint64(round)

	return &as64, nil
}

// toRoundBytes converts uint64 round to bytes
// Round begins with zero and it can be nil for backward compatibility.
// For that reason, Extra always has 8 bytes space for a round when the round has value.
func toRoundBytes(round uint64) []byte {
	roundBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(roundBytes, uint32(round))

	return roundBytes
}

// MarshalRLPTo defines the marshal function wrapper for IstanbulExtra
func (i *IstanbulExtra) MarshalRLPTo(dst []byte, mode EncodingMode) []byte {
	if i.isPalm {
		return types.MarshalRLPTo(i.marshalRLPWithPalm(mode), dst)
	} else {
		return types.MarshalRLPTo(i.marshalRLPWith, dst)
	}
}

// MarshalRLPWith defines the marshal function implementation for IstanbulExtra
func (i *IstanbulExtra) marshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	// Validators
	vv.Set(i.Validators.MarshalRLPWith(ar))

	// ProposerSeal
	if len(i.ProposerSeal) == 0 {
		vv.Set(ar.NewNull())
	} else {
		vv.Set(ar.NewCopyBytes(i.ProposerSeal))
	}

	// CommittedSeal
	vv.Set(i.CommittedSeals.MarshalRLPWith(ar))

	// ParentCommittedSeal
	if i.ParentCommittedSeals == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.ParentCommittedSeals.MarshalRLPWith(ar))
	}

	if i.RoundNumber == nil {
		vv.Set(ar.NewNull())
	} else {
		vv.Set(ar.NewBytes(
			toRoundBytes(*i.RoundNumber),
		))
	}

	return vv
}

// MarshalRLPWith defines the marshal function implementation for IstanbulExtra
func (i *IstanbulExtra) marshalRLPWithPalm(mode EncodingMode) func(*fastrlp.Arena) *fastrlp.Value {
	return func(ar *fastrlp.Arena) *fastrlp.Value {
		vv := ar.NewArray()

		// Vanity
		vv.Set(ar.NewCopyBytes(i.Vanity))

		// Validators
		vv.Set(i.Validators.MarshalRLPWith(ar))

		// Votes
		vv.Set(i.Vote.MarshalRLPWith(ar))

		if mode != ExcludeRoundAndCommitSeals {
			// Round
			if i.RoundNumber == nil {
				vv.Set(ar.NewCopyBytes(toRoundBytes(0)))
			} else {
				vv.Set(ar.NewBytes(
					toRoundBytes(*i.RoundNumber),
				))
			}

			// CommittedSeal
			if mode != ExcludeCommitSeals {
				// CommittedSeal
				vv.Set(i.CommittedSeals.MarshalRLPWith(ar))
			}
		}

		return vv
	}
}

// UnmarshalRLP defines the unmarshal function wrapper for IstanbulExtra
func (i *IstanbulExtra) UnmarshalRLP(input []byte) error {
	if i.isPalm {
		return types.UnmarshalRlp(i.unmarshalRLPFromPalm, input)
	} else {
		return types.UnmarshalRlp(i.unmarshalRLPFrom, input)
	}
}

// UnmarshalRLPFrom defines the unmarshal implementation for IstanbulExtra
func (i *IstanbulExtra) unmarshalRLPFromPalm(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) < 3 {
		return fmt.Errorf("incorrect number of elements to decode istambul extra, expected 3 but found %d", len(elems))
	}

	// Vanity
	if i.Vanity, err = elems[0].GetBytes(i.Vanity); err != nil {
		return fmt.Errorf("failed to get vanity bytes: %w", err)
	}

	// Validators
	if err := i.Validators.UnmarshalRLPFrom(p, elems[1]); err != nil {
		return err
	}

	// Votes
	if err := i.Vote.UnmarhsalRLPFrom(elems[2]); err != nil {
		return err
	}

	// Round
	round, err := parseRound(elems[3])
	if err != nil {
		return err
	}
	i.RoundNumber = round

	// CommittedSeal - if present
	if elems[4].Elems() > 0 {
		if err := i.CommittedSeals.UnmarshalRLPFrom(p, elems[4]); err != nil {
			return err
		}
	}

	return nil
}

// UnmarshalRLPFrom defines the unmarshal implementation for IstanbulExtra
func (i *IstanbulExtra) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) < 3 {
		return fmt.Errorf("incorrect number of elements to decode istambul extra, expected 3 but found %d", len(elems))
	}

	// Validators
	if err := i.Validators.UnmarshalRLPFrom(p, elems[0]); err != nil {
		return err
	}

	// ProposerSeal
	if i.ProposerSeal, err = elems[1].GetBytes(i.ProposerSeal); err != nil {
		return fmt.Errorf("failed to decode Seal: %w", err)
	}

	// CommittedSeal
	if err := i.CommittedSeals.UnmarshalRLPFrom(p, elems[2]); err != nil {
		return err
	}

	// ParentCommitted
	if len(elems) >= 4 && i.ParentCommittedSeals != nil {
		if err := i.ParentCommittedSeals.UnmarshalRLPFrom(p, elems[3]); err != nil {
			return err
		}
	}

	// Round
	if len(elems) >= 5 {
		roundNumber, err := parseRound(elems[4])
		if err != nil {
			return err
		}

		i.RoundNumber = roundNumber
	}

	return nil
}

// UnmarshalRLPForParentCS defines the unmarshal function wrapper for IstanbulExtra
// that parses only Parent Committed Seals
func (i *IstanbulExtra) unmarshalRLPForParentCS(input []byte) error {
	return types.UnmarshalRlp(i.unmarshalRLPFromForParentCS, input)
}

// UnmarshalRLPFrom defines the unmarshal implementation for IstanbulExtra
// that parses only Parent Committed Seals
func (i *IstanbulExtra) unmarshalRLPFromForParentCS(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	// ParentCommitted
	if len(elems) >= 4 {
		if err := i.ParentCommittedSeals.UnmarshalRLPFrom(p, elems[3]); err != nil {
			return err
		}
	}

	// Round
	if len(elems) >= 5 {
		roundNumber, err := parseRound(elems[4])
		if err != nil {
			return err
		}

		i.RoundNumber = roundNumber
	}

	return nil
}

// putIbftExtra sets the IBFT extra data field into the header
func putIbftExtra(h *types.Header, istanbulExtra *IstanbulExtra, mode EncodingMode) {
	if istanbulExtra.isPalm {
		// if there is a nil vanity here we need to just use an empty 30 bytes
		if istanbulExtra.Vanity == nil {
			istanbulExtra.Vanity = emptyVanity
		}

		h.ExtraData = istanbulExtra.MarshalRLPTo([]byte{}, mode)
	} else {
		// Pad zeros to the right up to istanbul vanity
		extra := h.ExtraData
		if len(extra) < IstanbulExtraVanity {
			extra = append(extra, zeroBytes[:IstanbulExtraVanity-len(extra)]...)
		} else {
			extra = extra[:IstanbulExtraVanity]
		}

		h.ExtraData = istanbulExtra.MarshalRLPTo(extra, EncodeEverything)
	}
}

// packFieldsIntoExtra is a helper function
// that injects a few fields into IBFT Extra
// without modifying other fields
// Validators, CommittedSeals, and ParentCommittedSeals have a few types
// and extra must have these instances before unmarshalling usually
// This function doesn't require the field instances that don't update
func packFieldsIntoExtra(
	extraBytes []byte,
	packFn func(
		ar *fastrlp.Arena,
		oldValues []*fastrlp.Value,
		newArrayValue *fastrlp.Value,
	) error,
) []byte {
	extraHeader := extraBytes[:IstanbulExtraVanity]
	extraBody := extraBytes[IstanbulExtraVanity:]

	newExtraBody := types.MarshalRLPTo(func(ar *fastrlp.Arena) *fastrlp.Value {
		vv := ar.NewArray()

		_ = types.UnmarshalRlp(func(p *fastrlp.Parser, v *fastrlp.Value) error {
			elems, err := v.GetElems()
			if err != nil {
				return err
			}

			if len(elems) < 3 {
				return fmt.Errorf("incorrect number of elements to decode istambul extra, expected 3 but found %d", len(elems))
			}

			return packFn(ar, elems, vv)
		}, extraBody)

		return vv
	}, nil)

	return append(
		extraHeader,
		newExtraBody...,
	)
}

// packProposerSealIntoExtra updates only Seal field in Extra
func packProposerSealIntoExtra(
	extraBytes []byte,
	proposerSeal []byte,
) []byte {
	return packFieldsIntoExtra(
		extraBytes,
		func(
			ar *fastrlp.Arena,
			oldValues []*fastrlp.Value,
			newArrayValue *fastrlp.Value,
		) error {
			// Validators
			newArrayValue.Set(oldValues[0])

			// Seal
			newArrayValue.Set(ar.NewBytes(proposerSeal))

			// CommittedSeal
			newArrayValue.Set(oldValues[2])

			// ParentCommittedSeal
			if len(oldValues) >= 4 {
				newArrayValue.Set(oldValues[3])
			}

			// Round
			if len(oldValues) >= 5 {
				newArrayValue.Set(oldValues[4])
			}

			return nil
		},
	)
}

// packCommittedSealsAndRoundNumberIntoExtra updates only CommittedSeal field in Extra
func packCommittedSealsAndRoundNumberIntoExtra(
	extraBytes []byte,
	committedSeal Seals,
	roundNumber *uint64,
) []byte {
	return packFieldsIntoExtra(
		extraBytes,
		func(
			ar *fastrlp.Arena,
			oldValues []*fastrlp.Value,
			newArrayValue *fastrlp.Value,
		) error {
			// Validators
			newArrayValue.Set(oldValues[0])

			// Seal
			newArrayValue.Set(oldValues[1])

			// CommittedSeal
			newArrayValue.Set(committedSeal.MarshalRLPWith(ar))

			// ParentCommittedSeal
			if len(oldValues) >= 4 {
				newArrayValue.Set(oldValues[3])
			} else {
				newArrayValue.Set(ar.NewNullArray())
			}

			if roundNumber == nil {
				newArrayValue.Set(ar.NewNull())
			} else {
				newArrayValue.Set(ar.NewBytes(
					toRoundBytes(*roundNumber),
				))
			}

			return nil
		},
	)
}

type Vote struct {
	Recipient types.Address
	VoteType  uint8
}

func (v *Vote) UnmarhsalRLPFrom(v2 *fastrlp.Value) error {
	if v2.Type() != fastrlp.TypeArray {
		return nil
	}

	elems, err := v2.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("invalid vote length: %d", len(elems))
	}

	recipient, err := elems[0].GetBytes(nil)
	if err != nil {
		return err
	}
	v.Recipient = types.BytesToAddress(recipient)

	voteType, err := elems[1].GetBytes(nil)
	if err != nil {
		return err
	}
	v.VoteType = voteType[0]

	return nil
}

func (v *Vote) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	if v == nil {
		return ar.NewCopyBytes([]byte{})
	} else if v.Recipient != types.ZeroAddress {
		newArr := ar.NewArray()
		newArr.Set(ar.NewCopyBytes(v.Recipient.Bytes()))
		newArr.Set(ar.NewCopyBytes([]byte{v.VoteType}))
		return newArr
	} else {
		return ar.NewCopyBytes([]byte{})
	}
}
