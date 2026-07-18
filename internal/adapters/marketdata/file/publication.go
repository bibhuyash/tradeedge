package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func (r Repository) Lineage(
	ctx context.Context,
	id storage.DatasetID,
	limit int,
) ([]storage.DatasetManifest, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var result []storage.DatasetManifest
	for id != "" && len(result) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reader, err := r.Open(ctx, id)
		if err != nil {
			return nil, err
		}
		manifest := reader.Manifest()
		_ = reader.Close()
		result = append(result, manifest)
		id = manifest.ParentID
	}
	return result, nil
}

func (r Repository) Publish(
	ctx context.Context,
	request storage.PublicationRequest,
) (storage.Publication, error) {
	if err := validatePublicationRequest(request); err != nil {
		return storage.Publication{}, err
	}
	reader, err := r.Open(ctx, request.DatasetID)
	if err != nil {
		return storage.Publication{}, err
	}
	_ = reader.Close()
	history, err := r.publications(ctx, request.Series)
	if err != nil && !errors.Is(err, storage.ErrDatasetNotFound) {
		return storage.Publication{}, err
	}
	for _, existing := range history {
		if existing.RequestID == request.RequestID {
			if existing.DatasetID == request.DatasetID &&
				existing.Action == request.Action &&
				existing.Reason == strings.TrimSpace(request.Reason) {
				return existing, nil
			}
			return storage.Publication{}, storage.ErrPublicationConflict
		}
	}
	var current storage.DatasetID
	var generation int64
	if len(history) > 0 {
		current = history[len(history)-1].DatasetID
		generation = history[len(history)-1].Generation
	}
	if current != request.ExpectedCurrentID {
		return storage.Publication{}, storage.ErrPublicationConflict
	}
	publication := storage.Publication{
		Series: request.Series, Generation: generation + 1,
		DatasetID: request.DatasetID, PreviousDatasetID: current,
		Action: request.Action, Reason: strings.TrimSpace(request.Reason),
		RequestID: strings.TrimSpace(request.RequestID), PublishedAt: request.PublishedAt.UTC(),
	}
	seriesRoot := filepath.Join(r.Root, "publications", request.Series)
	if err := os.MkdirAll(seriesRoot, 0o750); err != nil {
		return storage.Publication{}, err
	}
	tempDir, err := os.MkdirTemp(seriesRoot, ".building-")
	if err != nil {
		return storage.Publication{}, err
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	data, err := json.MarshalIndent(publication, "", "  ")
	if err != nil {
		cleanup()
		return storage.Publication{}, err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(tempDir, "publication.json"), data, 0o640); err != nil {
		cleanup()
		return storage.Publication{}, err
	}
	digest := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(tempDir, "publication.sha256"),
		[]byte(hex.EncodeToString(digest[:])+"\n"), 0o640); err != nil {
		cleanup()
		return storage.Publication{}, err
	}
	finalDir := filepath.Join(seriesRoot, fmt.Sprintf("%020d", publication.Generation))
	if err := os.Rename(tempDir, finalDir); err != nil {
		cleanup()
		if _, statErr := os.Stat(finalDir); statErr == nil {
			return storage.Publication{}, storage.ErrPublicationConflict
		}
		return storage.Publication{}, err
	}
	return publication, nil
}

func (r Repository) CurrentPublication(
	ctx context.Context,
	series string,
) (storage.Publication, error) {
	history, err := r.publications(ctx, series)
	if err != nil {
		return storage.Publication{}, err
	}
	return history[len(history)-1], nil
}

func (r Repository) Publications(
	ctx context.Context,
	series string,
	limit int,
) ([]storage.Publication, error) {
	history, err := r.publications(ctx, series)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start := 0
	if len(history) > limit {
		start = len(history) - limit
	}
	result := append([]storage.Publication(nil), history[start:]...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

func (r Repository) publications(
	ctx context.Context,
	series string,
) ([]storage.Publication, error) {
	if !validSeries(series) {
		return nil, storage.ErrDatasetNotFound
	}
	seriesRoot := filepath.Join(r.Root, "publications", series)
	entries, err := os.ReadDir(seriesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, storage.ErrDatasetNotFound
	}
	if err != nil {
		return nil, err
	}
	var generations []int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		generation, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err == nil && generation > 0 {
			generations = append(generations, generation)
		}
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	if len(generations) == 0 {
		return nil, storage.ErrDatasetNotFound
	}
	history := make([]storage.Publication, 0, len(generations))
	for index, generation := range generations {
		if generation != int64(index+1) {
			return nil, storage.ErrDatasetCorrupt
		}
		dir := filepath.Join(seriesRoot, fmt.Sprintf("%020d", generation))
		data, err := os.ReadFile(filepath.Join(dir, "publication.json"))
		if err != nil {
			return nil, storage.ErrDatasetCorrupt
		}
		wantBytes, err := os.ReadFile(filepath.Join(dir, "publication.sha256"))
		if err != nil {
			return nil, storage.ErrDatasetCorrupt
		}
		digest := sha256.Sum256(data)
		if strings.TrimSpace(string(wantBytes)) != hex.EncodeToString(digest[:]) {
			return nil, storage.ErrDatasetCorrupt
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var publication storage.Publication
		if err := decoder.Decode(&publication); err != nil {
			return nil, storage.ErrDatasetCorrupt
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
			publication.Series != series || publication.Generation != generation ||
			publication.DatasetID == "" || publication.Reason == "" ||
			publication.RequestID == "" || publication.PublishedAt.IsZero() ||
			(publication.Action != storage.PublicationPublish &&
				publication.Action != storage.PublicationRollback) ||
			(index == 0 && publication.PreviousDatasetID != "") ||
			(index > 0 && publication.PreviousDatasetID != history[index-1].DatasetID) {
			return nil, storage.ErrDatasetCorrupt
		}
		reader, err := r.Open(ctx, publication.DatasetID)
		if err != nil {
			return nil, storage.ErrDatasetCorrupt
		}
		_ = reader.Close()
		history = append(history, publication)
	}
	return history, nil
}

func validatePublicationRequest(request storage.PublicationRequest) error {
	if !validSeries(request.Series) || request.DatasetID == "" ||
		strings.TrimSpace(request.Reason) == "" || strings.TrimSpace(request.RequestID) == "" ||
		request.PublishedAt.IsZero() ||
		(request.Action != storage.PublicationPublish && request.Action != storage.PublicationRollback) {
		return storage.ErrPublicationConflict
	}
	return nil
}

func validSeries(series string) bool {
	if series == "" || len(series) > 64 {
		return false
	}
	for _, char := range series {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
