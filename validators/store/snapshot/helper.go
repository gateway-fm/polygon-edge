package snapshot

import (
	"github.com/umbracle/fastrlp"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
)

type AuthorizeResult int

const (
	YesAuthorize          = AuthorizeResult(iota)
	NoAuthorize           = AuthorizeResult(iota)
	InconclusiveAuthorize = AuthorizeResult(iota)
)

// isAuthorize is a helper function to return the bool value from Nonce
func isAuthorize(
	header *types.Header,
	candidateType validators.ValidatorType,
	isPalm bool,
) (AuthorizeResult, validators.Validator, error) {
	if isPalm {
		// work with raw RLP here rather than parsing all the values
		pr := fastrlp.DefaultParserPool.Get()
		defer fastrlp.DefaultParserPool.Put(pr)

		v, err := pr.Parse(header.ExtraData)
		if err != nil {
			return InconclusiveAuthorize, nil, err
		}

		elems, err := v.GetElems()
		if err != nil {
			return InconclusiveAuthorize, nil, ErrIncorrectExtraData
		}

		if len(elems) != 5 {
			return InconclusiveAuthorize, nil, ErrIncorrectExtraData
		}

		vote := elems[2]
		if vote.Type() != fastrlp.TypeArray {
			// this signifies there was no vote here
			return InconclusiveAuthorize, nil, nil
		}

		elems, err = vote.GetElems()
		if err != nil {
			return InconclusiveAuthorize, nil, ErrIncorrectExtraData
		}

		candidate, err := elems[0].GetBytes(nil)
		if err != nil {
			return InconclusiveAuthorize, nil, ErrIncorrectExtraData
		}

		voteType, err := elems[1].GetByte()
		if err != nil {
			return InconclusiveAuthorize, nil, ErrIncorrectExtraData
		}

		validator, err := minerToValidator(candidateType, candidate)
		if err != nil {
			return InconclusiveAuthorize, nil, err
		}

		if voteType == byte(255) {
			return YesAuthorize, validator, nil
		} else {
			return NoAuthorize, validator, nil
		}
	} else {
		validator, err := minerToValidator(candidateType, header.Miner)
		if err != nil {
			return InconclusiveAuthorize, nil, err
		}

		switch header.Nonce {
		case nonceAuthVote:
			return YesAuthorize, validator, nil
		case nonceDropVote:
			return NoAuthorize, validator, nil
		default:
			return InconclusiveAuthorize, nil, ErrIncorrectNonce
		}
	}
}

// shouldProcessVote is a helper function to return
// the flag indicating whether vote should be processed or not
// based on vote action and validator set
func shouldProcessVote(
	validators validators.Validators,
	candidate types.Address,
	voteAction bool, // true => add, false => remove
) bool {
	// if vote action is...
	// true  => validator set expects not to have a candidate
	// false => validator set expects     to have a candidate

	return voteAction != validators.Includes(candidate)
}

// addsOrDelsCandidate is a helper function to add/remove candidate to/from validators
func addsOrDelsCandidate(
	validators validators.Validators,
	candidate validators.Validator,
	updateAction bool,
) error {
	if updateAction {
		return validators.Add(candidate)
	} else {
		return validators.Del(candidate)
	}
}
