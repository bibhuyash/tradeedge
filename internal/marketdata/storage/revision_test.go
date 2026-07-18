package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestImmutableRevisionPublicationAndRollback(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository()
	root := commitDraft(t, repository, DraftManifest{
		MasterVersion: "master-v1", CalendarVersion: "calendar-v1", Source: "fixture",
		OrderingVersion: "ordering-v1", CreatedAt: time.Unix(1, 0),
	})
	child := commitDraft(t, repository, DraftManifest{
		ParentID: root.ID, MasterVersion: "master-v1", CalendarVersion: "calendar-v1",
		Source: "corrected-fixture", OrderingVersion: "ordering-v1", CreatedAt: time.Unix(2, 0),
		CorrectionReason: "late official correction", RequestID: "correction-1", Series: "nse-quotes",
	})
	lineage, err := repository.Lineage(ctx, child.ID, 10)
	if err != nil || len(lineage) != 2 || lineage[1].ID != root.ID {
		t.Fatalf("Lineage() = %#v, %v", lineage, err)
	}
	first, err := repository.Publish(ctx, PublicationRequest{
		Series: "nse-quotes", DatasetID: root.ID, Action: PublicationPublish,
		Reason: "initial", RequestID: "publish-1", PublishedAt: time.Unix(3, 0),
	})
	if err != nil {
		t.Fatalf("Publish(root) error = %v", err)
	}
	second, err := repository.Publish(ctx, PublicationRequest{
		Series: "nse-quotes", DatasetID: child.ID, ExpectedCurrentID: root.ID,
		Action: PublicationPublish, Reason: "correction", RequestID: "publish-2", PublishedAt: time.Unix(4, 0),
	})
	if err != nil || second.Generation != first.Generation+1 {
		t.Fatalf("Publish(child) = %#v, %v", second, err)
	}
	if _, err := repository.Publish(ctx, PublicationRequest{
		Series: "nse-quotes", DatasetID: root.ID, ExpectedCurrentID: root.ID,
		Action: PublicationRollback, Reason: "stale expected current", RequestID: "rollback-bad",
		PublishedAt: time.Unix(5, 0),
	}); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("Publish(conflict) error = %v", err)
	}
	rollback, err := repository.Publish(ctx, PublicationRequest{
		Series: "nse-quotes", DatasetID: root.ID, ExpectedCurrentID: child.ID,
		Action: PublicationRollback, Reason: "rollback correction", RequestID: "rollback-1",
		PublishedAt: time.Unix(6, 0),
	})
	if err != nil || rollback.Generation != 3 {
		t.Fatalf("Publish(rollback) = %#v, %v", rollback, err)
	}
	current, err := repository.CurrentPublication(ctx, "nse-quotes")
	if err != nil || current.DatasetID != root.ID {
		t.Fatalf("CurrentPublication() = %#v, %v", current, err)
	}
}

func TestBuildKeyIsDeterministicAndExcludesCreationTime(t *testing.T) {
	left := DraftManifest{
		ParentID: "parent", Revision: 2, MasterVersion: "m", CalendarVersion: "c",
		SourceSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OrderingVersion: "o", RequestID: "r", Series: "s", CreatedAt: time.Unix(1, 0),
	}
	right := left
	right.CreatedAt = time.Unix(999, 0)
	if ComputeBuildKey(left) != ComputeBuildKey(right) {
		t.Fatal("build key changed with creation time")
	}
}

func commitDraft(t *testing.T, repository *MemoryRepository, draft DraftManifest) DatasetManifest {
	t.Helper()
	writer, err := repository.Create(context.Background(), draft)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	manifest, err := writer.Commit(context.Background())
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return manifest
}
