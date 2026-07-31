// Package application coordinates Retrieval use cases without transport or infrastructure dependencies.
package application

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/indexprofile"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

var (
	ErrInvalidEvent            = errors.New("invalid retrieval event")
	ErrConflictingEvent        = errors.New("conflicting retrieval event")
	ErrUnsupportedIndexProfile = errors.New("unsupported index profile")
	ErrArtifactUnavailable     = errors.New("artifact unavailable")
	ErrLifecycleCleanupPending = errors.New("lifecycle cleanup pending")
)

type ManifestPolicy struct {
	MaxPages              uint32
	MaxShards             int
	MaxShardCompressed    int64
	MaxShardExpanded      int64
	MaxShardChunks        uint32
	MaxTotalChunks        uint32
	MaxExpandedTotalBytes int64
}

type MetadataPolicy struct {
	MaxTags int
}

type MetadataEvent struct {
	EventID, BookID, Title, Author, MediaType, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	Year                                                                                                           int
	LifecycleVersion                                                                                               uint64
	Tags                                                                                                           []string
	SourceSHA256, PayloadDigest                                                                                    [32]byte
	OccurredAt                                                                                                     time.Time
}

func (e MetadataEvent) Validate(policy MetadataPolicy) error {
	if !safeID(e.EventID) || !safeID(e.BookID) || strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Author) == "" ||
		e.Year < 0 || !supportedMediaType(e.MediaType) || e.SourceSHA256 == ([32]byte{}) || e.PayloadDigest == ([32]byte{}) || !safeID(e.CorrelationID) ||
		!safeID(e.CausationID) || e.Producer != "catalog-service" || e.SchemaVersion != "v1" || e.IdempotencyKey != e.BookID || e.OccurredAt.IsZero() || len(e.Tags) > policy.MaxTags {
		return ErrInvalidEvent
	}
	return nil
}

func (e MetadataEvent) ValidateEnvelope() error {
	if !safeID(e.EventID) || !safeID(e.BookID) || strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Author) == "" ||
		e.Year < 0 || !supportedMediaType(e.MediaType) || e.SourceSHA256 == ([32]byte{}) || e.PayloadDigest == ([32]byte{}) || !safeID(e.CorrelationID) ||
		!safeID(e.CausationID) || e.Producer != "catalog-service" || e.SchemaVersion != "v1" || e.IdempotencyKey != e.BookID || e.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	return nil
}

func supportedMediaType(value string) bool {
	return value == "" || value == domain.MediaTypePDF || value == domain.MediaTypeEPUB
}

func (e MetadataEvent) EffectiveMediaType() string {
	if e.MediaType == "" {
		return domain.MediaTypePDF
	}
	return e.MediaType
}

type Shard struct {
	Reference                          string
	SHA256                             [32]byte
	CompressedBytes, UncompressedBytes int64
	ChunkCount                         uint32
	FirstChunkOrder, LastChunkOrder    uint64
}

type ContentSelectionRange struct {
	Start, End uint32
	Reason     string
}

type ContentSelection struct {
	Mode, SelectorVersion, ParserVersion, FallbackReason string
	ModelDigest, PolicyDigest, ProcessingProfileDigest   [32]byte
	OriginalCount, RetainedCount, ExcludedCount          uint32
	ExcludedRatio                                        float64
	Ranges                                               []ContentSelectionRange
}

type Manifest struct {
	SchemaVersion, BookID, ExtractionVersion, NormalizationVersion, TokenizerVersion, ChunkingVersion, StructureVersion string
	SourceSHA256, ManifestSHA256, ProcessingConfigDigest                                                                [32]byte
	MaximumTokens, OverlapTokens, PageCount, ChunkCount                                                                 uint32
	LifecycleVersion                                                                                                    uint64
	GeneratedAt                                                                                                         time.Time
	Shards                                                                                                              []Shard
	ContentSelection                                                                                                    *ContentSelection
}

type ManifestEvent struct {
	EventID, BookID, ManifestReference, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	SourceSHA256, ManifestSHA256, PayloadDigest                                                             [32]byte
	LifecycleVersion                                                                                        uint64
	OccurredAt                                                                                              time.Time
	Manifest                                                                                                Manifest
}

// ValidateEnvelope verifies the trusted BookChunksReady descriptor without
// relying on the referenced manifest artifact.
func (e ManifestEvent) ValidateEnvelope() error {
	if !safeID(e.EventID) || !safeID(e.BookID) || !safeID(e.CorrelationID) || !safeID(e.CausationID) ||
		e.Producer != "ingestion-service" || e.SchemaVersion != "v1" || !safeID(e.IdempotencyKey) || !strings.HasPrefix(e.IdempotencyKey, e.BookID+":") || e.OccurredAt.IsZero() ||
		e.SourceSHA256 == ([32]byte{}) || e.ManifestSHA256 == ([32]byte{}) || e.PayloadDigest == ([32]byte{}) {
		return ErrInvalidEvent
	}
	idempotencyParts := strings.Split(e.IdempotencyKey, ":")
	if len(idempotencyParts) != 3 || idempotencyParts[0] != e.BookID || idempotencyParts[2] != "ready" || len(idempotencyParts[1]) != 64 {
		return ErrInvalidEvent
	}
	if _, err := hex.DecodeString(idempotencyParts[1]); err != nil {
		return ErrInvalidEvent
	}
	expectedDirectory := "books/" + e.BookID + "/" + hex.EncodeToString(e.SourceSHA256[:]) + "/" + idempotencyParts[1] + "/"
	if e.ManifestReference != expectedDirectory+"manifest.pb" || !validArtifactReference(e.ManifestReference) {
		return ErrInvalidEvent
	}
	return nil
}

func (e ManifestEvent) Validate(profile domain.IndexProfile, policy ManifestPolicy) error {
	if err := validateManifestPolicy(policy); err != nil || e.ValidateEnvelope() != nil || e.Manifest.BookID != e.BookID || e.Manifest.SourceSHA256 != e.SourceSHA256 || e.Manifest.ManifestSHA256 != e.ManifestSHA256 || len(e.Manifest.Shards) == 0 || len(e.Manifest.Shards) > policy.MaxShards {
		return ErrInvalidEvent
	}
	if effectiveLifecycleVersion(e.LifecycleVersion) != effectiveLifecycleVersion(e.Manifest.LifecycleVersion) {
		return ErrInvalidEvent
	}
	idempotencyParts := strings.Split(e.IdempotencyKey, ":")
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
	if !validContentSelection(e.Manifest, profile) {
		return ErrUnsupportedIndexProfile
	}
	if e.Manifest.PageCount < 1 || e.Manifest.PageCount > policy.MaxPages || e.Manifest.ChunkCount < 1 || e.Manifest.GeneratedAt.IsZero() || e.Manifest.GeneratedAt.After(e.OccurredAt) {
		return ErrInvalidEvent
	}
	var totalChunks uint32
	var totalUncompressed int64
	var nextChunkOrder uint64
	for index, shard := range e.Manifest.Shards {
		expectedReference := expectedDirectory + "shards/" + fmt.Sprintf("%06d.pb.zst", index)
		if shard.Reference != expectedReference || !validArtifactReference(shard.Reference) ||
			shard.SHA256 == ([32]byte{}) || shard.CompressedBytes < 1 || shard.CompressedBytes > policy.MaxShardCompressed || shard.UncompressedBytes < 1 || shard.UncompressedBytes > policy.MaxShardExpanded || shard.ChunkCount < 1 || shard.ChunkCount > policy.MaxShardChunks {
			return ErrInvalidEvent
		}
		expectedLastOrder, validOrder := shardLastOrder(nextChunkOrder, shard.ChunkCount)
		if !validOrder || shard.FirstChunkOrder != nextChunkOrder || shard.LastChunkOrder != expectedLastOrder {
			return ErrInvalidEvent
		}
		nextChunkOrder = shard.LastChunkOrder + 1
		if totalChunks > policy.MaxTotalChunks-shard.ChunkCount || totalUncompressed > policy.MaxExpandedTotalBytes-shard.UncompressedBytes {
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

func validContentSelection(manifest Manifest, profile domain.IndexProfile) bool {
	selection := manifest.ContentSelection
	if profile.ContentSelectionVersion == indexprofile.ContentSelectionDisabled {
		return selection == nil || selection.Mode == indexprofile.ContentSelectionDisabled
	}
	if selection == nil || selection.SelectorVersion != profile.ContentSelectionVersion ||
		(selection.Mode != "observation" && selection.Mode != "enforcement") ||
		selection.ParserVersion == "" || selection.ModelDigest == ([32]byte{}) ||
		selection.PolicyDigest == ([32]byte{}) || selection.ProcessingProfileDigest == ([32]byte{}) ||
		selection.ProcessingProfileDigest != manifest.ProcessingConfigDigest ||
		selection.OriginalCount != manifest.PageCount || selection.RetainedCount > selection.OriginalCount ||
		selection.ExcludedCount > selection.OriginalCount || math.IsNaN(selection.ExcludedRatio) ||
		math.IsInf(selection.ExcludedRatio, 0) || selection.ExcludedRatio < 0 || selection.ExcludedRatio > 1 {
		return false
	}
	var excluded uint32
	var previousEnd uint32
	for index, value := range selection.Ranges {
		if value.Start == 0 || value.End < value.Start || value.End > selection.OriginalCount ||
			!validContentSelectionReason(value.Reason) || (index > 0 && value.Start <= previousEnd) {
			return false
		}
		count := value.End - value.Start + 1
		if excluded > ^uint32(0)-count {
			return false
		}
		excluded += count
		previousEnd = value.End
	}
	if selection.Mode == "enforcement" {
		if selection.FallbackReason == "none" {
			expectedRatio := float64(selection.ExcludedCount) / float64(selection.OriginalCount)
			return excluded == selection.ExcludedCount &&
				selection.RetainedCount+selection.ExcludedCount == selection.OriginalCount &&
				math.Abs(selection.ExcludedRatio-expectedRatio) <= 1e-12
		}
		return validContentSelectionFallback(selection.FallbackReason) && selection.FallbackReason != "observation" &&
			len(selection.Ranges) == 0 && selection.ExcludedCount == 0 && selection.RetainedCount == selection.OriginalCount && selection.ExcludedRatio == 0
	}
	return selection.FallbackReason == "observation" && len(selection.Ranges) == 0 &&
		selection.RetainedCount == selection.OriginalCount && selection.ExcludedCount == 0 && selection.ExcludedRatio == 0
}

func validContentSelectionFallback(value string) bool {
	switch value {
	case "none", "observation", "unsupported_layout", "processing_timeout", "invalid_output", "ambiguous_mapping", "resource_limit", "excessive_exclusion", "internal_error":
		return true
	default:
		return false
	}
}

func validContentSelectionReason(value string) bool {
	switch value {
	case "title_page", "copyright_or_imprint", "dedication_or_ornamental_blank", "table_or_list", "index", "publisher_catalog_or_advertising", "also_by", "colophon":
		return true
	default:
		return false
	}
}

// ManifestFailureCategory identifies terminal failures that can be safely
// reported from a validated descriptor even when its artifact is corrupt.
func ManifestFailureCategory(event ManifestEvent, err error) (domain.FailureCategory, bool) {
	if event.ValidateEnvelope() != nil {
		return "", false
	}
	if errors.Is(err, ErrUnsupportedIndexProfile) {
		return domain.FailureIncompatibleProfile, true
	}
	if errors.Is(err, ErrInvalidEvent) {
		return domain.FailureManifestIntegrity, true
	}
	return "", false
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

func validateManifestPolicy(policy ManifestPolicy) error {
	if policy.MaxPages < 1 || policy.MaxShards < 1 || policy.MaxShardCompressed < 1 || policy.MaxShardExpanded < 1 ||
		policy.MaxShardChunks < 1 || policy.MaxTotalChunks < 1 || policy.MaxExpandedTotalBytes < 1 {
		return errors.New("invalid manifest policy")
	}
	if policy.MaxShardCompressed > policy.MaxShardExpanded || policy.MaxShardExpanded > policy.MaxExpandedTotalBytes || policy.MaxShardChunks > policy.MaxTotalChunks {
		return errors.New("invalid manifest policy")
	}
	return nil
}

func validateMetadataPolicy(policy MetadataPolicy) error {
	if policy.MaxTags < 1 {
		return errors.New("invalid metadata policy")
	}
	return nil
}

type PlanningSnapshot struct {
	Metadata *MetadataEvent
	Manifest *ManifestEvent
	Planned  bool
}
type BatchPlan struct {
	JobID, BatchID, BookID, Reference                                                            string
	SHA256                                                                                       [32]byte
	CompressedBytes, UncompressedBytes                                                           int64
	ChunkCount, ManifestPageCount, MaximumTokens, OverlapTokens                                  uint32
	FirstChunkOrder, LastChunkOrder                                                              uint64
	ExtractionVersion, NormalizationVersion, TokenizerVersion, ChunkingVersion, StructureVersion string
	ProfileDigest                                                                                [32]byte
	LifecycleVersion                                                                             uint64
	OccurredAt                                                                                   time.Time
}

type PlanningRepository interface {
	ProjectMetadata(context.Context, MetadataEvent) (PlanningSnapshot, error)
	ProjectManifest(context.Context, ManifestEvent) (PlanningSnapshot, error)
	CommitPlan(context.Context, PlanningSnapshot, []BatchPlan) (bool, error)
}

type Planner struct {
	repository     PlanningRepository
	newID          func() (string, error)
	now            func() time.Time
	profile        domain.IndexProfile
	metadataPolicy MetadataPolicy
	manifestPolicy ManifestPolicy
}

func NewPlanner(repository PlanningRepository, newID func() (string, error), now func() time.Time, metadataPolicy MetadataPolicy, manifestPolicy ManifestPolicy) (*Planner, error) {
	if repository == nil || newID == nil || now == nil || validateMetadataPolicy(metadataPolicy) != nil || validateManifestPolicy(manifestPolicy) != nil {
		return nil, errors.New("invalid planner configuration")
	}
	return &Planner{repository: repository, newID: newID, now: now, profile: domain.SupportedIndexProfile(), metadataPolicy: metadataPolicy, manifestPolicy: manifestPolicy}, nil
}

func (p *Planner) HandleMetadata(ctx context.Context, event MetadataEvent) error {
	if err := event.Validate(p.metadataPolicy); err != nil {
		return err
	}
	snapshot, err := p.repository.ProjectMetadata(ctx, event)
	if err != nil {
		return err
	}
	return p.plan(ctx, snapshot)
}

func (p *Planner) HandleManifest(ctx context.Context, event ManifestEvent) error {
	profile, ok := domain.SupportedIndexProfileForExtraction(event.Manifest.ExtractionVersion)
	if !ok {
		return ErrUnsupportedIndexProfile
	}
	if err := event.Validate(profile, p.manifestPolicy); err != nil {
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
	profile, ok := domain.SupportedIndexProfileForExtraction(snapshot.Manifest.Manifest.ExtractionVersion)
	if !ok || !profileMatchesMediaType(profile, snapshot.Metadata.EffectiveMediaType()) {
		return ErrUnsupportedIndexProfile
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
			UncompressedBytes: shard.UncompressedBytes, ChunkCount: shard.ChunkCount, ManifestPageCount: snapshot.Manifest.Manifest.PageCount,
			FirstChunkOrder: shard.FirstChunkOrder, LastChunkOrder: shard.LastChunkOrder, ExtractionVersion: snapshot.Manifest.Manifest.ExtractionVersion,
			NormalizationVersion: snapshot.Manifest.Manifest.NormalizationVersion, TokenizerVersion: snapshot.Manifest.Manifest.TokenizerVersion,
			ChunkingVersion: snapshot.Manifest.Manifest.ChunkingVersion, StructureVersion: snapshot.Manifest.Manifest.StructureVersion,
			MaximumTokens: snapshot.Manifest.Manifest.MaximumTokens, OverlapTokens: snapshot.Manifest.Manifest.OverlapTokens,
			ProfileDigest: profile.Digest, LifecycleVersion: effectiveLifecycleVersion(snapshot.Manifest.LifecycleVersion), OccurredAt: now}
	}
	_, err = p.repository.CommitPlan(ctx, snapshot, batches)
	return err
}

func profileMatchesMediaType(profile domain.IndexProfile, mediaType string) bool {
	switch mediaType {
	case domain.MediaTypePDF:
		return profile.ExtractionVersion == indexprofile.ExtractionPDF || profile.ExtractionVersion == indexprofile.ExtractionPDFFiltered
	case domain.MediaTypeEPUB:
		return profile.ExtractionVersion == indexprofile.ExtractionEPUB || profile.ExtractionVersion == indexprofile.ExtractionEPUBFiltered
	default:
		return false
	}
}

func effectiveLifecycleVersion(value uint64) uint64 {
	if value == 0 {
		return 1
	}
	return value
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
