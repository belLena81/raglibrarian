// Package transport maps bounded protobuf events to Retrieval application types.
package transport

import (
	"crypto/sha256"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"google.golang.org/protobuf/proto"
)

var defaultManifestPolicy = application.ManifestPolicy{
	MaxPages:              1000,
	MaxShards:             2048,
	MaxShardCompressed:    32 << 20,
	MaxShardExpanded:      64 << 20,
	MaxShardChunks:        256,
	MaxTotalChunks:        50_000,
	MaxExpandedTotalBytes: 2 << 30,
}

func ManifestReference(payload []byte) (string, error) {
	event, err := DecodeManifestEnvelope(payload)
	if err != nil {
		return "", err
	}
	return event.ManifestReference, nil
}

func DecodeManifestEnvelope(payload []byte) (application.ManifestEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	var message ingestionv1.BookChunksReadyV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil || len(message.SourceSha256) != sha256.Size || len(message.ManifestSha256) != sha256.Size ||
		message.OccurredAt == nil || !message.OccurredAt.IsValid() || message.ManifestByteSize < 1 || message.ManifestByteSize > int64(contracts.MaximumManifestBytes) {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	lifecycleVersion, ok := decodeLifecycleVersion(message.LifecycleVersion)
	if !ok {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	event := application.ManifestEvent{EventID: message.EventId, BookID: message.BookId, ManifestReference: message.ManifestReference, SourceSHA256: bytesToDigest(message.SourceSha256),
		ManifestSHA256: bytesToDigest(message.ManifestSha256), PayloadDigest: sha256.Sum256(payload), CorrelationID: message.CorrelationId, CausationID: message.CausationId,
		Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey, OccurredAt: message.OccurredAt.AsTime(), LifecycleVersion: lifecycleVersion}
	if event.ValidateEnvelope() != nil {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	return event, nil
}

func DecodeMetadata(payload []byte) (application.MetadataEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.MetadataEvent{}, application.ErrInvalidEvent
	}
	var message catalogv1.BookUploadedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil || len(message.Sha256) != sha256.Size || message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return application.MetadataEvent{}, application.ErrInvalidEvent
	}
	lifecycleVersion, ok := decodeLifecycleVersion(message.LifecycleVersion)
	if !ok {
		return application.MetadataEvent{}, application.ErrInvalidEvent
	}
	event := application.MetadataEvent{EventID: message.EventId, BookID: message.BookId, Title: message.Title, Author: message.Author, MediaType: message.MediaType, Year: int(message.Year), Tags: append([]string{}, message.Tags...),
		CorrelationID: message.CorrelationId, CausationID: message.CausationId, Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey,
		OccurredAt: message.OccurredAt.AsTime(), PayloadDigest: sha256.Sum256(payload), LifecycleVersion: lifecycleVersion}
	copy(event.SourceSHA256[:], message.Sha256)
	return event, event.ValidateEnvelope()
}

func DecodeManifest(payload, manifestPayload []byte) (application.ManifestEvent, error) {
	event, err := DecodeManifestEnvelope(payload)
	if err != nil {
		return application.ManifestEvent{}, err
	}
	var message ingestionv1.BookChunksReadyV1
	if err = (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	if len(manifestPayload) == 0 || len(manifestPayload) > contracts.MaximumManifestBytes || int64(len(manifestPayload)) != message.ManifestByteSize || sha256.Sum256(manifestPayload) != event.ManifestSHA256 {
		return event, application.ErrInvalidEvent
	}
	var manifestMessage ingestionv1.ChunkManifestV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(manifestPayload, &manifestMessage); err != nil || len(manifestMessage.SourceSha256) != sha256.Size || len(manifestMessage.Shards) == 0 {
		return event, application.ErrInvalidEvent
	}
	if len(manifestMessage.ProcessingConfigDigest) != sha256.Size || manifestMessage.GeneratedAt == nil || !manifestMessage.GeneratedAt.IsValid() ||
		manifestMessage.PageCount != message.PageCount || manifestMessage.ChunkCount != message.ChunkCount || manifestMessage.ExtractionVersion != message.ExtractionVersion ||
		manifestMessage.NormalizationVersion != message.NormalizationVersion || manifestMessage.TokenizerVersion != message.TokenizerVersion ||
		manifestMessage.ChunkingVersion != message.ChunkingVersion || manifestMessage.StructureVersion != message.StructureVersion ||
		manifestMessage.MaximumTokens != message.MaximumTokens || manifestMessage.OverlapTokens != message.OverlapTokens {
		return event, application.ErrInvalidEvent
	}
	manifestLifecycleVersion, ok := decodeLifecycleVersion(manifestMessage.LifecycleVersion)
	if !ok {
		return event, application.ErrInvalidEvent
	}
	manifest := application.Manifest{SchemaVersion: manifestMessage.SchemaVersion, BookID: manifestMessage.BookId, SourceSHA256: bytesToDigest(manifestMessage.SourceSha256), ManifestSHA256: bytesToDigest(message.ManifestSha256),
		ProcessingConfigDigest: bytesToDigest(manifestMessage.ProcessingConfigDigest), PageCount: manifestMessage.PageCount, ChunkCount: manifestMessage.ChunkCount, GeneratedAt: manifestMessage.GeneratedAt.AsTime(),
		ExtractionVersion: manifestMessage.ExtractionVersion, NormalizationVersion: manifestMessage.NormalizationVersion, TokenizerVersion: manifestMessage.TokenizerVersion,
		ChunkingVersion: manifestMessage.ChunkingVersion, StructureVersion: manifestMessage.StructureVersion, MaximumTokens: manifestMessage.MaximumTokens, OverlapTokens: manifestMessage.OverlapTokens,
		LifecycleVersion: manifestLifecycleVersion, Shards: make([]application.Shard, len(manifestMessage.Shards))}
	for index, shard := range manifestMessage.Shards {
		if shard == nil || len(shard.Sha256) != sha256.Size {
			return event, application.ErrInvalidEvent
		}
		manifest.Shards[index] = application.Shard{Reference: shard.Reference, SHA256: bytesToDigest(shard.Sha256), CompressedBytes: shard.CompressedByteSize, UncompressedBytes: shard.UncompressedByteSize,
			ChunkCount: shard.ChunkCount, FirstChunkOrder: shard.FirstChunkOrder, LastChunkOrder: shard.LastChunkOrder}
	}
	if manifestMessage.ContentSelection != nil {
		selection, selectionErr := mapContentSelection(manifestMessage.ContentSelection)
		if selectionErr != nil {
			return event, application.ErrInvalidEvent
		}
		manifest.ContentSelection = selection
	}
	event.Manifest = manifest
	profile, ok := domain.SupportedIndexProfileForExtraction(manifest.ExtractionVersion)
	if !ok {
		return event, application.ErrUnsupportedIndexProfile
	}
	return event, event.Validate(profile, defaultManifestPolicy)
}

func mapContentSelection(value *ingestionv1.ContentSelectionV1) (*application.ContentSelection, error) {
	if len(value.ModelDigest) != sha256.Size || len(value.PolicyDigest) != sha256.Size ||
		len(value.ProcessingProfileDigest) != sha256.Size || len(value.ExcludedRanges) > int(defaultManifestPolicy.MaxPages) {
		return nil, application.ErrInvalidEvent
	}
	mode, ok := mapContentSelectionMode(value.Mode)
	if !ok {
		return nil, application.ErrInvalidEvent
	}
	fallbackReason, ok := mapContentSelectionFallback(value.FallbackReason)
	if !ok {
		return nil, application.ErrInvalidEvent
	}
	selection := &application.ContentSelection{
		Mode:                    mode,
		SelectorVersion:         value.SelectorVersion,
		ParserVersion:           value.ParserVersion,
		FallbackReason:          fallbackReason,
		ModelDigest:             bytesToDigest(value.ModelDigest),
		PolicyDigest:            bytesToDigest(value.PolicyDigest),
		ProcessingProfileDigest: bytesToDigest(value.ProcessingProfileDigest),
		OriginalCount:           value.OriginalOrdinalCount,
		RetainedCount:           value.RetainedOrdinalCount,
		ExcludedCount:           value.ExcludedOrdinalCount,
		ExcludedRatio:           value.ExcludedRatio,
		Ranges:                  make([]application.ContentSelectionRange, len(value.ExcludedRanges)),
	}
	for index, excludedRange := range value.ExcludedRanges {
		if excludedRange == nil {
			return nil, application.ErrInvalidEvent
		}
		reason, reasonOK := mapExclusionReason(excludedRange.Reason)
		if !reasonOK {
			return nil, application.ErrInvalidEvent
		}
		selection.Ranges[index] = application.ContentSelectionRange{
			Start:  excludedRange.StartOrdinal,
			End:    excludedRange.EndOrdinal,
			Reason: reason,
		}
	}
	return selection, nil
}

func mapContentSelectionMode(value ingestionv1.ContentSelectionMode) (string, bool) {
	switch value {
	case ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_DISABLED:
		return "disabled", true
	case ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION:
		return "observation", true
	case ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT:
		return "enforcement", true
	default:
		return "", false
	}
}

func mapContentSelectionFallback(value ingestionv1.ContentSelectionFallbackReason) (string, bool) {
	name, ok := ingestionv1.ContentSelectionFallbackReason_name[int32(value)]
	if !ok || value == ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_UNSPECIFIED {
		return "", false
	}
	return strings.ToLower(strings.TrimPrefix(name, "CONTENT_SELECTION_FALLBACK_REASON_")), true
}

func mapExclusionReason(value ingestionv1.ContentExclusionReason) (string, bool) {
	name, ok := ingestionv1.ContentExclusionReason_name[int32(value)]
	if !ok || value == ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_UNSPECIFIED {
		return "", false
	}
	return strings.ToLower(strings.TrimPrefix(name, "CONTENT_EXCLUSION_REASON_")), true
}

func DecodeBatch(payload []byte) (application.BatchWork, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.BatchWork{}, application.ErrInvalidEvent
	}
	var message retrievalv1.IndexBatchRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil || len(message.ShardSha256) != sha256.Size || len(message.SourceSha256) != sha256.Size ||
		len(message.ManifestSha256) != sha256.Size || len(message.IndexProfileDigest) != sha256.Size || message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return application.BatchWork{}, application.ErrInvalidEvent
	}
	lifecycleVersion, ok := decodeLifecycleVersion(message.LifecycleVersion)
	if !ok {
		return application.BatchWork{}, application.ErrInvalidEvent
	}
	work := application.BatchWork{EventID: message.EventId, JobID: message.JobId, BatchID: message.BatchId, BookID: message.BookId, ShardReference: message.ShardReference,
		ShardSHA256: bytesToDigest(message.ShardSha256), SourceSHA256: bytesToDigest(message.SourceSha256), ManifestSHA256: bytesToDigest(message.ManifestSha256), ProfileDigest: bytesToDigest(message.IndexProfileDigest),
		CompressedBytes: message.CompressedByteSize, UncompressedBytes: message.UncompressedByteSize, ChunkCount: message.ChunkCount, ManifestPageCount: message.ManifestPageCount,
		FirstChunkOrder: message.FirstChunkOrder, LastChunkOrder: message.LastChunkOrder, ExtractionVersion: message.ExtractionVersion,
		NormalizationVersion: message.NormalizationVersion, TokenizerVersion: message.TokenizerVersion, ChunkingVersion: message.ChunkingVersion,
		StructureVersion: message.StructureVersion, MaximumTokens: message.MaximumTokens, OverlapTokens: message.OverlapTokens, CorrelationID: message.CorrelationId,
		CausationID: message.CausationId, Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey, OccurredAt: message.OccurredAt.AsTime(), LifecycleVersion: lifecycleVersion}
	return work, work.Validate(defaultManifestPolicy)
}

func DecodeReindex(payload []byte) (application.LifecycleEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.LifecycleEvent{}, application.ErrInvalidEvent
	}
	var message catalogv1.BookReindexRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil ||
		len(message.SourceSha256) != sha256.Size || len(message.ManifestSha256) != sha256.Size ||
		message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return application.LifecycleEvent{}, application.ErrInvalidEvent
	}
	lifecycleVersion, ok := decodeLifecycleVersion(message.LifecycleVersion)
	if !ok {
		return application.LifecycleEvent{}, application.ErrInvalidEvent
	}
	event := application.LifecycleEvent{
		EventID: message.EventId, BookID: message.BookId, CommandID: message.CommandId, ActorID: message.ActorId,
		ManifestReference: message.ManifestReference, Kind: application.LifecycleReindex, LifecycleVersion: lifecycleVersion,
		SourceSHA256: bytesToDigest(message.SourceSha256), ManifestSHA256: bytesToDigest(message.ManifestSha256),
		PayloadDigest: sha256.Sum256(payload), CorrelationID: message.CorrelationId, CausationID: message.CausationId,
		Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey,
		OccurredAt: message.OccurredAt.AsTime(),
	}
	return event, event.Validate()
}

func DecodeDeletion(payload []byte) (application.LifecycleEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.LifecycleEvent{}, application.ErrInvalidEvent
	}
	var message catalogv1.BookDeletionRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil ||
		message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return application.LifecycleEvent{}, application.ErrInvalidEvent
	}
	lifecycleVersion, ok := decodeLifecycleVersion(message.LifecycleVersion)
	if !ok {
		return application.LifecycleEvent{}, application.ErrInvalidEvent
	}
	event := application.LifecycleEvent{
		EventID: message.EventId, BookID: message.BookId, CommandID: message.CommandId, ActorID: message.ActorId,
		Kind: application.LifecycleDelete, LifecycleVersion: lifecycleVersion, PayloadDigest: sha256.Sum256(payload),
		CorrelationID: message.CorrelationId, CausationID: message.CausationId, Producer: message.Producer,
		SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey, OccurredAt: message.OccurredAt.AsTime(),
	}
	return event, event.Validate()
}

func decodeLifecycleVersion(value int64) (uint64, bool) {
	if value < 0 {
		return 0, false
	}
	if value == 0 {
		return 1, true
	}
	return uint64(value), true
}

func bytesToDigest(value []byte) [sha256.Size]byte {
	var result [sha256.Size]byte
	copy(result[:], value)
	return result
}
