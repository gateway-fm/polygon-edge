package signer

import (
	"github.com/umbracle/fastrlp"
)

type SecpSeals struct {
	Signatures [][65]byte
}

func (s *SecpSeals) Num() int {
	return len(s.Signatures)
}

func (s *SecpSeals) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	x := ar.NewArray()

	for _, sig := range s.Signatures {
		x.Set(ar.NewCopyBytes(sig[:]))
	}

	return x
}

func (s *SecpSeals) UnmarshalRLPFrom(parser *fastrlp.Parser, value *fastrlp.Value) error {
	vals, err := value.GetElems()

	if err != nil {
		return err
	}

	for _, v := range vals {
		var sig [65]byte

		if _, err := v.GetBytes(sig[:]); err != nil {
			return err
		}

		s.Signatures = append(s.Signatures, sig)
	}

	return nil
}
