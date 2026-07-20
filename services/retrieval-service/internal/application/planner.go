// Package application coordinates Retrieval use cases without transport or infrastructure dependencies.
package application

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

var (
	ErrInvalidEvent            = errors.New("invalid retrieval event")
	ErrConflictingEvent        = errors.New("conflicting retrieval event")
	ErrUnsupportedIndexProfile = errors.New("unsupported index profile")
)

type MetadataEvent struct {
	EventID, BookID, Title, Author, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	Year                                                                                                int
	Tags                                                                                                []string
	SourceSHA256, PayloadDigest                                                                         [32]byte
	OccurredAt                                                                                          time.Time
}

func (e MetadataEvent) Validate() error {
	if !safeID(e.EventID) || !safeID(e.BookID) || strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Author) == "" ||
		e.Year < 0 || e.SourceSHA256 == ([32]byte{}) || e.PayloadDigest == ([32]byte{}) || !safeID(e.CorrelationID) ||
		!safeID(e.CausationID) || e.Producer != "catalog-service" || e.SchemaVersion != "v1" || e.IdempotencyKey != e.BookID || e.OccurredAt.IsZero() || len(e.Tags) > domain.MaximumFilterTags {
		return ErrInvalidEvent
	}
	return nil
}

type Shard struct {
	Reference                          string
	SHA256                             [32]byte
	CompressedBytes, UncompressedBytes int64
	ChunkCount                         uint32
	FirstChunkOrder, LastChunkOrder    uint64
}

type Manifest struct {
	SchemaVersion, BookID, ExtractionVersion, NormalizationVersion, TokenizerVersion, ChunkingVersion, StructureVersion string
	SourceSHA256, ManifestSHA256, ProcessingConfigDigest                                                                [32]byte
	MaximumTokens, OverlapTokens, PageCount, ChunkCount                                                                 uint32
	GeneratedAt                                                                                                         time.Time
	Shards                                                                                                              []Shard
}

type ManifestEvent struct {
	EventID, BookID, ManifestReference, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	SourceSHA256, ManifestSHA256, PayloadDigest                                                             [32]byte
	OccurredAt                                                                                              time.Time
	Manifest                                                                                                Manifest
}

func (e ManifestEvent) Validate(profile domain.IndexProfile) error {
	if !safeID(e.EventID) || !safeID(e.BookID) || !safeID(e.CorrelationID) || !safeID(e.CausationID) ||
		e.Producer != "ingestion-service" || e.SchemaVersion != "v1" || !safeID(e.IdempotencyKey) || !strings.HasPrefix(e.IdempotencyKey, e.BookID+":") || e.OccurredAt.IsZero() ||
		e.SourceSHA256 == ([32]byte{}) || e.ManifestSHA256 == ([32]byte{}) || e.PayloadDigest == ([32]byte{}) ||
		e.Manifest.BookID != e.BookID || e.Manifest.SourceSHA256 != e.SourceSHA256 || e.Manifest.ManifestSHA256 != e.ManifestSHA256 || len(e.Manifest.Shards) == 0 || len(e.Manifest.Shards) > 2048 {
		return ErrInvalidEvent
	}
	idempotencyParts := strings.Split(e.IdempotencyKey, ":")
	if len(idempotencyParts) != 3 || idempotencyParts[0] != e.BookID || idempotencyParts[2] != "ready" || len(idempotencyParts[1]) != 64 {
		return ErrInvalidEvent
	}
	processingDigest, decodeErr := hex.DecodeString(idempotencyParts[1])
	if decodeErr != nil || len(processingDigest) != 32 || string(processingDigest) != string(e.Manifest.ProcessingConfigDigest[:]) {
		return ErrInvalidEvent
	}
	expectedDirectory := "books/" + e.BookID + "/" + hex.EncodeToString(e.SourceSHA256[:]) + "/" + idempotencyParts[1] + "/"
	if e.ManifestReference != expectedDirectory+"manifest.pb" || !validArtifactReference(e.ManifestReference) {
		return ErrInvalidEvent
	}
	if e.Manifest.SchemaVersion != profile.ManifestSchema || e.Manifest.ExtractionVersion != profile.ExtractionVersion ||
		e.Manifest.NormalizationVersion != profile.NormalizationVersion || e.Manifest.TokenizerVersion != profile.TokenizerVersion ||
		e.Manifest.ChunkingVersion != profile.ChunkingVersion || e.Manifest.StructureVersion != profile.StructureVersion {
		return ErrUnsupportedIndexProfile
	}
	if !matchesProfileNumbers(e.Manifest, profile) {
		return ErrUnsupportedIndexProfile
	}
	if e.Manifest.PageCount < 1 || e.Manifest.ChunkCount < 1 || e.Manifest.GeneratedAt.IsZero() || e.Manifest.GeneratedAt.After(e.OccurredAt) {
		return ErrInvalidEvent
	}
	var totalChunks uint32
	var totalUncompressed int64
	var nextChunkOrder uint64
	for index, shard := range e.Manifest.Shards {
		expectedReference := expectedDirectory + "shards/" + fmt.Sprintf("%06d.pb.zst", index)
		if shard.Reference != expectedReference || !validArtifactReference(shard.Reference) ||
			shard.SHA256 == ([32]byte{}) || shard.CompressedBytes < 1 || shard.CompressedBytes > 32<<20 || shard.UncompressedBytes < 1 || shard.UncompressedBytes > 64<<20 || shard.ChunkCount < 1 || shard.ChunkCount > 256 {
			return ErrInvalidEvent
		}
		expectedLastOrder, validOrder := shardLastOrder(nextChunkOrder, shard.ChunkCount)
		if !validOrder || shard.FirstChunkOrder != nextChunkOrder || shard.LastChunkOrder != expectedLastOrder {
			return ErrInvalidEvent
		}
		nextChunkOrder = shard.LastChunkOrder + 1
		if totalChunks > 50_000-shard.ChunkCount || totalUncompressed > (2<<30)-shard.UncompressedBytes {
			return ErrInvalidEvent
		}
		totalChunks += shard.ChunkCount
		totalUncompressed += shard.UncompressedBytes
	}
	if totalChunks != e.Manifest.ChunkCount {
		return ErrInvalidEvent
	}
	return nil
}

func matchesProfileNumbers(manifest Manifest, profile domain.IndexProfile) bool {
	if profile.MaximumTokens < 0 || profile.OverlapTokens < 0 {
		return false
	}
	return uint64(manifest.MaximumTokens) == uint64(profile.MaximumTokens) && // #nosec G115 -- negative profile values are rejected above.
		uint64(manifest.OverlapTokens) == uint64(profile.OverlapTokens) // #nosec G115 -- negative profile values are rejected above.
}

func shardLastOrder(first uint64, count uint32) (uint64, bool) {
	if count == 0 || first > ^uint64(0)-uint64(count-1) {
		return 0, false
	}
	return first + uint64(count) - 1, true
}

type PlanningSnapshot struct {
	Metadata *MetadataEvent
	Manifest *ManifestEvent
	Planned  bool
}
type BatchPlan struct {
	JobID, BatchID, BookID, Reference  string
	SHA256                             [32]byte
	CompressedBytes, UncompressedBytes int64
	ChunkCount                         uint32
	ProfileDigest                      [32]byte
	OccurredAt                         time.Time
}

type PlanningRepository interface {
	ProjectMetadata(context.Context, MetadataEvent) (PlanningSnapshot, error)
	ProjectManifest(context.Context, ManifestEvent) (PlanningSnapshot, error)
	CommitPlan(context.Context, PlanningSnapshot, []BatchPlan) (bool, error)
}

type Planner struct {
	repository PlanningRepository
	newID      func() (string, error)
	now        func() time.Time
	profile    domain.IndexProfile
}

func NewPlanner(repository PlanningRepository, newID func() (string, error), now func() time.Time) (*Planner, error) {
	if repository == nil || newID == nil || now == nil {
		return nil, errors.New("invalid planner configuration")
	}
	return &Planner{repository: repository, newID: newID, now: now, profile: domain.SupportedIndexProfile()}, nil
}

func (p *Planner) HandleMetadata(ctx context.Context, event MetadataEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	snapshot, err := p.repository.ProjectMetadata(ctx, event)
	if err != nil {
		return err
	}
	return p.plan(ctx, snapshot)
}

func (p *Planner) HandleManifest(ctx context.Context, event ManifestEvent) error {
	if err := event.Validate(p.profile); err != nil {
		return err
	}
	snapshot, err := p.repository.ProjectManifest(ctx, event)
	if err != nil {
		return err
	}
	return p.plan(ctx, snapshot)
}

func (p *Planner) plan(ctx context.Context, snapshot PlanningSnapshot) error {
	if snapshot.Planned || snapshot.Metadata == nil || snapshot.Manifest == nil {
		return nil
	}
	if snapshot.Metadata.BookID != snapshot.Manifest.BookID || snapshot.Metadata.SourceSHA256 != snapshot.Manifest.SourceSHA256 {
		return ErrConflictingEvent
	}
	jobID, err := p.newID()
	if err != nil || !safeID(jobID) {
		return errors.New("generate indexing identity")
	}
	batches := make([]BatchPlan, len(snapshot.Manifest.Manifest.Shards))
	now := p.now().UTC()
	for index, shard := range snapshot.Manifest.Manifest.Shards {
		batches[index] = BatchPlan{JobID: jobID, BatchID: jobID + ":" + stringID(index), BookID: snapshot.Metadata.BookID,
			Reference: shard.Reference, SHA256: shard.SHA256, CompressedBytes: shard.CompressedBytes,
			UncompressedBytes: shard.UncompressedBytes, ChunkCount: shard.ChunkCount, ProfileDigest: p.profile.Digest, OccurredAt: now}
	}
	_, err = p.repository.CommitPlan(ctx, snapshot, batches)
	return err
}

func safeID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != ':' {
			return false
		}
	}
	return true
}

func validArtifactReference(value string) bool {
	return strings.HasPrefix(value, "books/") && len(value) <= 1024 && !strings.Contains(value, "..") && !strings.ContainsAny(value, "\x00\r\n")
}

func stringID(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	result := make([]byte, 0, 8)
	for value > 0 {
		result = append(result, digits[value%10])
		value /= 10
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return string(result)
}
