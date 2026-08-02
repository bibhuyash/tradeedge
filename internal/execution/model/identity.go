package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrInvalidIdentity = errors.New("invalid execution identity")

type digest [sha256.Size]byte

func derive(namespace string, parts ...string) digest {
	hash := sha256.New()
	writePart(hash, namespace)
	for _, part := range parts {
		writePart(hash, part)
	}
	var value digest
	copy(value[:], hash.Sum(nil))
	return value
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

type ExecutionIntentID digest
type OrderPlanID digest
type OrderLegID digest
type OrderID digest
type ClientOrderID digest
type SubmissionAttemptID digest
type ExecutionReportID digest
type FillID digest
type PublicationID digest
type StateChecksum digest

func newID(namespace string, parts ...string) (digest, error) {
	if !validParts(parts) {
		return digest{}, ErrInvalidIdentity
	}
	return derive(namespace, parts...), nil
}

func NewExecutionReportID(source, eventID string) (ExecutionReportID, error) {
	value, err := newID("execution-report-id/v1", source, eventID)
	return ExecutionReportID(value), err
}
func NewFillID(source, executionID string) (FillID, error) {
	value, err := newID("execution-fill-id/v1", source, executionID)
	return FillID(value), err
}
func NewPublicationID(parts ...string) (PublicationID, error) {
	value, err := newID("oms-publication-id/v1", parts...)
	return PublicationID(value), err
}
func NewSubmissionAttemptID(parts ...string) (SubmissionAttemptID, error) {
	value, err := newID("submission-attempt-id/v1", parts...)
	return SubmissionAttemptID(value), err
}
func NewStateChecksum(canonical []byte) (StateChecksum, error) {
	if len(canonical) == 0 {
		return StateChecksum{}, ErrInvalidIdentity
	}
	return StateChecksum(derive("execution-state-checksum/v1", string(canonical))), nil
}

func digestString(value digest) string { return hex.EncodeToString(value[:]) }

func (value ExecutionIntentID) String() string   { return digestString(digest(value)) }
func (value OrderPlanID) String() string         { return digestString(digest(value)) }
func (value OrderLegID) String() string          { return digestString(digest(value)) }
func (value OrderID) String() string             { return digestString(digest(value)) }
func (value ClientOrderID) String() string       { return digestString(digest(value)) }
func (value SubmissionAttemptID) String() string { return digestString(digest(value)) }
func (value ExecutionReportID) String() string   { return digestString(digest(value)) }
func (value FillID) String() string              { return digestString(digest(value)) }
func (value PublicationID) String() string       { return digestString(digest(value)) }
func (value StateChecksum) String() string       { return digestString(digest(value)) }
func (value ExecutionIntentID) IsZero() bool     { return value == ExecutionIntentID{} }
func (value OrderPlanID) IsZero() bool           { return value == OrderPlanID{} }
func (value OrderLegID) IsZero() bool            { return value == OrderLegID{} }
func (value OrderID) IsZero() bool               { return value == OrderID{} }
func (value ClientOrderID) IsZero() bool         { return value == ClientOrderID{} }
func (value SubmissionAttemptID) IsZero() bool   { return value == SubmissionAttemptID{} }
func (value ExecutionReportID) IsZero() bool     { return value == ExecutionReportID{} }
func (value FillID) IsZero() bool                { return value == FillID{} }
func (value PublicationID) IsZero() bool         { return value == PublicationID{} }
func (value StateChecksum) IsZero() bool         { return value == StateChecksum{} }

type OrderRevision uint64

func (value OrderRevision) Validate() error {
	if value == 0 {
		return ErrInvalidIdentity
	}
	return nil
}
