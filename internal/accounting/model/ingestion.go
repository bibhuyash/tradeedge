package model

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var ErrInvalidIngestion = errors.New("invalid accounting ingestion metadata")

type AccountBinding struct {
	PortfolioID portfoliomodel.PortfolioID
	AccountID   domain.AccountID
	Version     string
	ValidFrom   time.Time
	checksum    StateChecksum
}

func NewAccountBinding(portfolioID portfoliomodel.PortfolioID, accountID domain.AccountID, version string, validFrom time.Time) (AccountBinding, error) {
	version = strings.TrimSpace(version)
	if portfolioID.IsZero() || accountID == "" || version == "" || validFrom.IsZero() {
		return AccountBinding{}, ErrInvalidIngestion
	}
	raw, _ := json.Marshal(struct{ PortfolioID, AccountID, Version, ValidFrom string }{portfolioID.String(), string(accountID), version, validFrom.UTC().Format(time.RFC3339Nano)})
	checksum, _ := NewStateChecksum("account-binding/v1", raw)
	return AccountBinding{PortfolioID: portfolioID, AccountID: accountID, Version: version, ValidFrom: validFrom.UTC(), checksum: checksum}, nil
}

func (value AccountBinding) Checksum() StateChecksum { return value.checksum }
func (value AccountBinding) IsZero() bool {
	return value.PortfolioID.IsZero() || value.AccountID == "" || value.checksum.IsZero()
}
func (value AccountBinding) Validate() error {
	rebuilt, err := NewAccountBinding(value.PortfolioID, value.AccountID, value.Version, value.ValidFrom)
	if err != nil || rebuilt.checksum != value.checksum {
		return ErrInvalidIngestion
	}
	return nil
}

type IngestionMetadata struct {
	ID               IngestionID
	SourceSequence   uint64
	SourceCheckpoint string
	SourceChecksum   StateChecksum
	Binding          AccountBinding
}

func NewIngestionMetadata(fill executionmodel.Fill, sequence uint64, checkpoint string, sourceChecksum StateChecksum, binding AccountBinding) (IngestionMetadata, error) {
	checkpoint = strings.TrimSpace(checkpoint)
	if fill.ID().IsZero() || sequence == 0 || checkpoint == "" || sourceChecksum.IsZero() || binding.Validate() != nil {
		return IngestionMetadata{}, ErrInvalidIngestion
	}
	id, err := NewIngestionID(fill.ID().String(), checkpoint, binding.Checksum().String())
	if err != nil {
		return IngestionMetadata{}, err
	}
	return IngestionMetadata{ID: id, SourceSequence: sequence, SourceCheckpoint: checkpoint, SourceChecksum: sourceChecksum, Binding: binding}, nil
}

type IngestionProgress struct {
	Metadata           IngestionMetadata
	FillID             executionmodel.FillID
	FillChecksum       StateChecksum
	PositionID         PositionID
	PositionRevision   PositionRevision
	ApplicationID      FillApplicationID
	CheckpointChecksum StateChecksum
	checksum           StateChecksum
	canonical          []byte
}

func NewIngestionProgress(value IngestionProgress) (IngestionProgress, error) {
	if value.Metadata.ID.IsZero() || value.FillID.IsZero() || value.FillChecksum.IsZero() || value.PositionID.IsZero() ||
		value.PositionRevision.Validate() != nil || value.ApplicationID.IsZero() || value.CheckpointChecksum.IsZero() {
		return IngestionProgress{}, ErrInvalidIngestion
	}
	raw, err := json.Marshal(struct {
		ID, SourceCheckpoint, SourceChecksum, BindingChecksum, FillID, FillChecksum, PositionID, ApplicationID, CheckpointChecksum string
		SourceSequence, PositionRevision                                                                                           uint64
	}{value.Metadata.ID.String(), value.Metadata.SourceCheckpoint, value.Metadata.SourceChecksum.String(), value.Metadata.Binding.Checksum().String(), value.FillID.String(), value.FillChecksum.String(), value.PositionID.String(), value.ApplicationID.String(), value.CheckpointChecksum.String(), value.Metadata.SourceSequence, uint64(value.PositionRevision)})
	if err != nil {
		return IngestionProgress{}, ErrInvalidIngestion
	}
	value.checksum, _ = NewStateChecksum("accounting-ingestion-progress/v1", raw)
	value.canonical = raw
	return value, nil
}

func (value IngestionProgress) ID() IngestionID         { return value.Metadata.ID }
func (value IngestionProgress) Checksum() StateChecksum { return value.checksum }
func (value IngestionProgress) CanonicalJSON() []byte   { return append([]byte(nil), value.canonical...) }
func (value IngestionProgress) IsZero() bool            { return value.Metadata.ID.IsZero() }
