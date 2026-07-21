// Package transport maps bounded protobuf events to Retrieval application types.
package transport

import (
	"crypto/sha256"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"google.golang.org/protobuf/proto"
)

const maximumEventBytes = 256 << 10

func ManifestReference(payload []byte) (string, error) {
	event, err := DecodeManifestEnvelope(payload)
	if err != nil {
		return "", err
	}
	return event.ManifestReference, nil
}

func DecodeManifestEnvelope(payload []byte) (application.ManifestEvent, error) {
	if len(payload) == 0 || len(payload) > maximumEventBytes {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	var message ingestionv1.BookChunksReadyV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil || len(message.SourceSha256) != sha256.Size || len(message.ManifestSha256) != sha256.Size ||
		message.OccurredAt == nil || !message.OccurredAt.IsValid() || message.ManifestByteSize < 1 || message.ManifestByteSize > 4<<20 {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	event := application.ManifestEvent{EventID: message.EventId, BookID: message.BookId, ManifestReference: message.ManifestReference, SourceSHA256: bytesToDigest(message.SourceSha256),
		ManifestSHA256: bytesToDigest(message.ManifestSha256), PayloadDigest: sha256.Sum256(payload), CorrelationID: message.CorrelationId, CausationID: message.CausationId,
		Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey, OccurredAt: message.OccurredAt.AsTime()}
	if event.ValidateEnvelope() != nil {
		return application.ManifestEvent{}, application.ErrInvalidEvent
	}
	return event, nil
}

func DecodeMetadata(payload []byte) (application.MetadataEvent, error) {
	if len(payload) == 0 || len(payload) > maximumEventBytes {
		return application.MetadataEvent{}, application.ErrInvalidEvent
	}
	var message catalogv1.BookUploadedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil || len(message.Sha256) != sha256.Size || message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return application.MetadataEvent{}, application.ErrInvalidEvent
	}
	event := application.MetadataEvent{EventID: message.EventId, BookID: message.BookId, Title: message.Title, Author: message.Author, Year: int(message.Year), Tags: append([]string{}, message.Tags...),
		CorrelationID: message.CorrelationId, CausationID: message.CausationId, Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey,
		OccurredAt: message.OccurredAt.AsTime(), PayloadDigest: sha256.Sum256(payload)}
	copy(event.SourceSHA256[:], message.Sha256)
	return event, event.Validate()
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
	if len(manifestPayload) == 0 || len(manifestPayload) > 4<<20 || int64(len(manifestPayload)) != message.ManifestByteSize || sha256.Sum256(manifestPayload) != event.ManifestSHA256 {
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
	manifest := application.Manifest{SchemaVersion: manifestMessage.SchemaVersion, BookID: manifestMessage.BookId, SourceSHA256: bytesToDigest(manifestMessage.SourceSha256), ManifestSHA256: bytesToDigest(message.ManifestSha256),
		ProcessingConfigDigest: bytesToDigest(manifestMessage.ProcessingConfigDigest), PageCount: manifestMessage.PageCount, ChunkCount: manifestMessage.ChunkCount, GeneratedAt: manifestMessage.GeneratedAt.AsTime(),
		ExtractionVersion: manifestMessage.ExtractionVersion, NormalizationVersion: manifestMessage.NormalizationVersion, TokenizerVersion: manifestMessage.TokenizerVersion,
		ChunkingVersion: manifestMessage.ChunkingVersion, StructureVersion: manifestMessage.StructureVersion, MaximumTokens: manifestMessage.MaximumTokens, OverlapTokens: manifestMessage.OverlapTokens,
		Shards: make([]application.Shard, len(manifestMessage.Shards))}
	for index, shard := range manifestMessage.Shards {
		if shard == nil || len(shard.Sha256) != sha256.Size {
			return event, application.ErrInvalidEvent
		}
		manifest.Shards[index] = application.Shard{Reference: shard.Reference, SHA256: bytesToDigest(shard.Sha256), CompressedBytes: shard.CompressedByteSize, UncompressedBytes: shard.UncompressedByteSize,
			ChunkCount: shard.ChunkCount, FirstChunkOrder: shard.FirstChunkOrder, LastChunkOrder: shard.LastChunkOrder}
	}
	event.Manifest = manifest
	return event, event.Validate(domain.SupportedIndexProfile())
}

func DecodeBatch(payload []byte) (application.BatchWork, error) {
	if len(payload) == 0 || len(payload) > maximumEventBytes {
		return application.BatchWork{}, application.ErrInvalidEvent
	}
	var message retrievalv1.IndexBatchRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil || len(message.ShardSha256) != sha256.Size || len(message.SourceSha256) != sha256.Size ||
		len(message.ManifestSha256) != sha256.Size || len(message.IndexProfileDigest) != sha256.Size || message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return application.BatchWork{}, application.ErrInvalidEvent
	}
	work := application.BatchWork{EventID: message.EventId, JobID: message.JobId, BatchID: message.BatchId, BookID: message.BookId, ShardReference: message.ShardReference,
		ShardSHA256: bytesToDigest(message.ShardSha256), SourceSHA256: bytesToDigest(message.SourceSha256), ManifestSHA256: bytesToDigest(message.ManifestSha256), ProfileDigest: bytesToDigest(message.IndexProfileDigest),
		CompressedBytes: message.CompressedByteSize, UncompressedBytes: message.UncompressedByteSize, ChunkCount: message.ChunkCount, CorrelationID: message.CorrelationId,
		CausationID: message.CausationId, Producer: message.Producer, SchemaVersion: message.SchemaVersion, IdempotencyKey: message.IdempotencyKey, OccurredAt: message.OccurredAt.AsTime()}
	return work, work.Validate()
}

func bytesToDigest(value []byte) [sha256.Size]byte {
	var result [sha256.Size]byte
	copy(result[:], value)
	return result
}
