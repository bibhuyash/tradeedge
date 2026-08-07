package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrInvalidIdentity = errors.New("invalid accounting identity")

type digest [sha256.Size]byte

func derive(namespace string, parts ...string) digest {
	hash := sha256.New()
	writePart(hash, namespace)
	for _, part := range parts {
		writePart(hash, part)
	}
	var result digest
	copy(result[:], hash.Sum(nil))
	return result
}

type writer interface{ Write([]byte) (int, error) }

func writePart(output writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = output.Write(size[:])
	_, _ = output.Write([]byte(value))
}

func validParts(parts []string) bool {
	if len(parts) == 0 || len(parts) > 64 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || len(part) > 4096 {
			return false
		}
	}
	return true
}

type PositionID digest
type FillApplicationID digest
type PublicationID digest
type StateChecksum digest

func NewPositionID(portfolioID, instrumentID string) (PositionID, error) {
	if !validParts([]string{portfolioID, instrumentID}) {
		return PositionID{}, ErrInvalidIdentity
	}
	return PositionID(derive("accounting-position-id/v1", portfolioID, instrumentID)), nil
}

func NewFillApplicationID(positionID PositionID, fillID string) (FillApplicationID, error) {
	if positionID.IsZero() || !validParts([]string{fillID}) {
		return FillApplicationID{}, ErrInvalidIdentity
	}
	return FillApplicationID(derive("accounting-fill-application-id/v1", positionID.String(), fillID)), nil
}

func NewPublicationID(positionID PositionID, fillID string) (PublicationID, error) {
	if positionID.IsZero() || !validParts([]string{fillID}) {
		return PublicationID{}, ErrInvalidIdentity
	}
	return PublicationID(derive("accounting-publication-id/v1", positionID.String(), fillID)), nil
}

func NewStateChecksum(namespace string, canonical []byte) (StateChecksum, error) {
	if strings.TrimSpace(namespace) == "" || len(canonical) == 0 {
		return StateChecksum{}, ErrInvalidIdentity
	}
	return StateChecksum(derive(namespace, string(canonical))), nil
}

func digestString(value digest) string { return hex.EncodeToString(value[:]) }

func (value PositionID) String() string        { return digestString(digest(value)) }
func (value FillApplicationID) String() string { return digestString(digest(value)) }
func (value PublicationID) String() string     { return digestString(digest(value)) }
func (value StateChecksum) String() string     { return digestString(digest(value)) }
func (value PositionID) IsZero() bool          { return value == PositionID{} }
func (value FillApplicationID) IsZero() bool   { return value == FillApplicationID{} }
func (value PublicationID) IsZero() bool       { return value == PublicationID{} }
func (value StateChecksum) IsZero() bool       { return value == StateChecksum{} }

type PositionRevision uint64

func (value PositionRevision) Validate() error {
	if value == 0 {
		return ErrInvalidIdentity
	}
	return nil
}
