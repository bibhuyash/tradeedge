package storage

import (
	"bytes"
	"errors"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestCheckpointSerializationIsStableAndChecksummed(t *testing.T) {
	t.Parallel()
	checkpoint := checkpointFixture(t)
	first, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("checkpoint encoding is not deterministic")
	}
	decoded, err := DecodeCheckpoint(first)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Checksum() != checkpoint.Checksum() ||
		decoded.State().Hash() != checkpoint.State().Hash() {
		t.Fatal("decoded checkpoint differs from source")
	}
}

func TestCheckpointCorruptionFailsClosed(t *testing.T) {
	t.Parallel()
	encoded, err := EncodeCheckpoint(checkpointFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte(nil), encoded...)
	corrupted[len(corrupted)/2] ^= 1
	if _, err := DecodeCheckpoint(corrupted); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("DecodeCheckpoint() error = %v", err)
	}
}

func TestCheckpointRestorationExpectations(t *testing.T) {
	t.Parallel()
	checkpoint := checkpointFixture(t)
	expectation := RestoreExpectation{
		InstanceID: checkpoint.InstanceID(), DefinitionID: checkpoint.DefinitionID(),
		VersionID: checkpoint.VersionID(), ConfigurationHash: checkpoint.ConfigurationHash(),
		InstanceRevisionID: checkpoint.InstanceRevisionID(),
		StateSchemaVersion: checkpoint.State().SchemaVersion(), Revision: checkpoint.Revision(),
	}
	if err := VerifyRestoration(checkpoint, expectation); err != nil {
		t.Fatalf("valid restoration: %v", err)
	}
	otherVersion, _ := strategymodel.NewVersionID(strategymodel.VersionManifest{
		DefinitionID: checkpoint.DefinitionID(), ImplementationVersion: "other/v1",
		InputContractVersion: "frame/v1", ConfigurationSchemaVersion: "config/v1",
		StateSchemaVersion: "state/v1", ResultSchemaVersion: "result/v1",
		ProposalSchemaVersion: "proposal/v1",
	})
	tests := []struct {
		name   string
		mutate func(*RestoreExpectation)
	}{
		{"version", func(value *RestoreExpectation) { value.VersionID = otherVersion }},
		{"configuration", func(value *RestoreExpectation) {
			value.ConfigurationHash = strategymodel.ConfigurationHash{1}
		}},
		{"schema", func(value *RestoreExpectation) { value.StateSchemaVersion = "state/v2" }},
		{"revision", func(value *RestoreExpectation) { value.Revision++ }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := expectation
			test.mutate(&value)
			if err := VerifyRestoration(checkpoint, value); !errors.Is(err, ErrCorruptCheckpoint) {
				t.Fatalf("VerifyRestoration() error = %v", err)
			}
		})
	}
}

func FuzzDecodeCheckpoint(f *testing.F) {
	checkpoint := checkpointFixture(f)
	encoded, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeCheckpoint(data)
		if err == nil {
			if verifyErr := VerifyCheckpoint(decoded); verifyErr != nil {
				t.Fatalf("accepted invalid checkpoint: %v", verifyErr)
			}
			first, encodeErr := EncodeCheckpoint(decoded)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			second, encodeErr := EncodeCheckpoint(decoded)
			if encodeErr != nil || !bytes.Equal(first, second) {
				t.Fatal("accepted checkpoint did not re-encode deterministically")
			}
		}
	})
}

type testingT interface {
	Helper()
	Fatal(...any)
}

func checkpointFixture(t testingT) RuntimeCheckpoint {
	t.Helper()
	definitionID, _ := strategymodel.NewDefinitionID("checkpoint-fixture")
	versionID, _ := strategymodel.NewVersionID(strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "fixture/v1",
		InputContractVersion: "frame/v1", ConfigurationSchemaVersion: "config/v1",
		StateSchemaVersion: "state/v1", ResultSchemaVersion: "result/v1",
		ProposalSchemaVersion: "proposal/v1",
	})
	instanceID, _ := domain.NewStrategyID("checkpoint-instance")
	configuration, _ := strategymodel.NewStrategyConfiguration("config/v1", []byte(`{"period":5}`))
	instanceRevision, _ := strategymodel.NewInstanceRevisionID(
		instanceID, versionID, configuration.Hash(), 1,
	)
	state, _ := strategymodel.NewStrategyRuntimeState("state/v1", []byte(`{"count":0}`))
	checkpoint, err := NewRuntimeCheckpoint(RuntimeCheckpointSpec{
		InstanceID: instanceID, DefinitionID: definitionID, VersionID: versionID,
		InstanceRevisionID: instanceRevision, ConfigurationHash: configuration.Hash(),
		State: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
