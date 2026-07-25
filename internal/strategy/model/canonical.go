package model

import (
	"errors"

	"github.com/bibhuyash/tradeedge/internal/canonicaljson"
)

var ErrInvalidCanonicalJSON = errors.New("invalid canonical JSON")

func canonicalJSONObject(raw []byte, maximum int) ([]byte, error) {
	value, err := canonicaljson.Object(raw, maximum)
	if err != nil {
		return nil, ErrInvalidCanonicalJSON
	}
	return value, nil
}
