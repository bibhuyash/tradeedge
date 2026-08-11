package marketvalidation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDay0ClosureClassificationPreservesPartialSessionBoundary(t *testing.T) {
	value := passingDay0Attestation()
	if got := classifyDay0Attestation(value, true, true, false); got != Day0PartialSessionPass {
		t.Fatalf("classification = %s", got)
	}
	if got := classifyDay0Attestation(value, true, true, true); got != Day0SessionPass {
		t.Fatalf("classification = %s", got)
	}
}

func TestDay0ClosureFailsOnAuthorityOrSafetyActivity(t *testing.T) {
	base := passingDay0Attestation()
	for name, mutate := range map[string]func(*Day0RuntimeAttestation){
		"order":     func(v *Day0RuntimeAttestation) { v.Orders = 1 },
		"mutation":  func(v *Day0RuntimeAttestation) { v.RealBrokerMutations = 1 },
		"bad exit":  func(v *Day0RuntimeAttestation) { v.ContainerExitCode = 1 },
		"not ready": func(v *Day0RuntimeAttestation) { v.RuntimeReady = false },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if got := classifyDay0Attestation(value, true, true, false); got != Day0SessionFail {
				t.Fatalf("classification = %s", got)
			}
		})
	}
}

func passingDay0Attestation() Day0RuntimeAttestation {
	return Day0RuntimeAttestation{StartupPass: true, RuntimeReady: true, MarketDataReady: true, RequiredInstruments: 2, CoveredInstruments: 2, NormalTradingObserved: true, SessionClosed: true, SessionClosedReason: "AFTER_CLOSE", EODCloseCompleted: true, CheckpointGenerations: 3, OperatorSocketMode: "0600", ShutdownPass: true}
}

func TestClosureEvidenceIsChecksumBoundAndConfinedToEvidenceRoot(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "2026-08-11")
	if err := os.MkdirAll(session, 0o750); err != nil {
		t.Fatal(err)
	}
	raw := []byte("safe evidence\n")
	path := filepath.Join(root, "checkpoint.json")
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}
	reference := EvidenceReference{Kind: "runtime_checkpoint", Path: "../checkpoint.json", SHA256: digest(raw)}
	if err := verifyClosureReference(session, reference); err != nil {
		t.Fatalf("valid reference: %v", err)
	}
	reference.SHA256 = digest([]byte("different"))
	if err := verifyClosureReference(session, reference); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	reference.Path, reference.SHA256 = "../../outside.json", digest(raw)
	if err := verifyClosureReference(session, reference); err == nil {
		t.Fatal("root escape accepted")
	}
}
