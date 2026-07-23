package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDecodeMetadataNormalizesMissingTagsToEmptySlice(t *testing.T) {
	source := sha256.Sum256([]byte("synthetic source"))
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&catalogv1.BookUploadedV1{
		EventId:         "event-1",
		BookId:          "book-1",
		Title:           "Tagless Book",
		Author:          "Author",
		Year:            2026,
		ObjectReference: "books/book-1/source.pdf",
		Sha256:          source[:],
		ByteSize:        128,
		MediaType:       "application/pdf",
		ActorId:         "actor-1",
		CorrelationId:   "correlation-1",
		CausationId:     "cause-1",
		Producer:        "catalog-service",
		SchemaVersion:   "v1",
		IdempotencyKey:  "book-1",
		OccurredAt:      timestamppb.New(time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}

	event, err := DecodeMetadata(payload)
	if err != nil {
		t.Fatalf("DecodeMetadata() error = %v", err)
	}
	if event.Tags == nil {
		t.Fatal("DecodeMetadata() tags = nil, want empty slice")
	}
	if len(event.Tags) != 0 {
		t.Fatalf("DecodeMetadata() tags = %#v, want empty slice", event.Tags)
	}
}

func TestDecodeManifestBindsOuterDescriptorAndProcessingIdentity(t *testing.T) {
	event, manifest := validManifestPayloads(t)
	if _, err := DecodeManifest(event, manifest); err != nil {
		t.Fatalf("DecodeManifest() error = %v", err)
	}

	var outer ingestionv1.BookChunksReadyV1
	if err := proto.Unmarshal(event, &outer); err != nil {
		t.Fatal(err)
	}
	outer.ChunkCount++
	conflicting, _ := proto.MarshalOptions{Deterministic: true}.Marshal(&outer)
	if _, err := DecodeManifest(conflicting, manifest); err == nil {
		t.Fatal("DecodeManifest() accepted conflicting outer chunk count")
	}
}

func TestDecodeManifestEnvelopeValidatesOuterDescriptorWithoutArtifact(t *testing.T) {
	payload, _ := validManifestPayloads(t)

	event, err := DecodeManifestEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeManifestEnvelope() error = %v", err)
	}
	if event.BookID != "book-1" || event.ManifestReference == "" || event.ManifestSHA256 == ([sha256.Size]byte{}) {
		t.Fatalf("DecodeManifestEnvelope() event = %#v", event)
	}
	if len(event.Manifest.Shards) != 0 {
		t.Fatal("DecodeManifestEnvelope() retained manifest artifact data")
	}
}

func TestDecodeManifestRetainsValidatedEnvelopeForCorruptArtifact(t *testing.T) {
	payload, manifest := validManifestPayloads(t)
	manifest[0] ^= 0xff

	event, err := DecodeManifest(payload, manifest)
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("DecodeManifest() error = %v, want invalid event", err)
	}
	if err := event.ValidateEnvelope(); err != nil {
		t.Fatalf("corrupt manifest lost validated envelope: %v", err)
	}
	if len(event.Manifest.Shards) != 0 {
		t.Fatal("corrupt manifest was retained")
	}
}

func TestDecodeManifestRejectsArtifactReceiptMismatch(t *testing.T) {
	payload, manifest := validManifestPayloads(t)
	var outer ingestionv1.BookChunksReadyV1
	if err := proto.Unmarshal(payload, &outer); err != nil {
		t.Fatal(err)
	}
	outer.ManifestByteSize++
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&outer)
	if err != nil {
		t.Fatal(err)
	}

	event, err := DecodeManifest(payload, manifest)
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("DecodeManifest() error = %v, want invalid event", err)
	}
	if err := event.ValidateEnvelope(); err != nil {
		t.Fatalf("receipt mismatch lost validated envelope: %v", err)
	}
	if len(event.Manifest.Shards) != 0 {
		t.Fatal("receipt mismatch retained manifest artifact data")
	}
}

func TestDecodeManifestRejectsInvalidOuterEventWithoutEnvelope(t *testing.T) {
	payload, manifest := validManifestPayloads(t)
	var outer ingestionv1.BookChunksReadyV1
	if err := proto.Unmarshal(payload, &outer); err != nil {
		t.Fatal(err)
	}
	outer.BookId = "invalid/book"
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&outer)
	if err != nil {
		t.Fatal(err)
	}

	event, err := DecodeManifest(payload, manifest)
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("DecodeManifest() error = %v, want invalid event", err)
	}
	if event.EventID != "" || event.BookID != "" || event.ManifestReference != "" {
		t.Fatalf("invalid outer event retained identity: %#v", event)
	}

	event, err = DecodeManifestEnvelope(payload)
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("DecodeManifestEnvelope() error = %v, want invalid event", err)
	}
	if event.EventID != "" || event.BookID != "" || event.ManifestReference != "" {
		t.Fatalf("invalid outer envelope retained identity: %#v", event)
	}
}

func TestDecodeBatchRetainsManifestValidationContract(t *testing.T) {
	profile := domain.SupportedIndexProfile()
	source := sha256.Sum256([]byte("synthetic source"))
	manifest := sha256.Sum256([]byte("synthetic manifest"))
	shard := sha256.Sum256([]byte("synthetic shard"))
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&retrievalv1.IndexBatchRequestedV1{
		EventId:              "batch-event-1",
		JobId:                "job-1",
		BatchId:              "job-1:0",
		BookId:               "book-1",
		ShardReference:       "books/book-1/profile/shards/000000.pb.zst",
		ShardSha256:          shard[:],
		CompressedByteSize:   128,
		UncompressedByteSize: 512,
		ChunkCount:           1,
		SourceSha256:         source[:],
		ManifestSha256:       manifest[:],
		IndexProfileDigest:   profile.Digest[:],
		FirstChunkOrder:      7,
		LastChunkOrder:       7,
		ManifestPageCount:    2,
		ExtractionVersion:    profile.ExtractionVersion,
		NormalizationVersion: profile.NormalizationVersion,
		TokenizerVersion:     profile.TokenizerVersion,
		ChunkingVersion:      profile.ChunkingVersion,
		StructureVersion:     profile.StructureVersion,
		MaximumTokens:        uint32(profile.MaximumTokens),
		OverlapTokens:        uint32(profile.OverlapTokens),
		CorrelationId:        "correlation-1",
		OccurredAt:           timestamppb.New(time.Date(2026, 7, 20, 9, 2, 0, 0, time.UTC)),
		CausationId:          "manifest-event-1",
		Producer:             "retrieval-service",
		SchemaVersion:        "v1",
		IdempotencyKey:       "job-1:0",
	})
	if err != nil {
		t.Fatal(err)
	}

	work, err := DecodeBatch(payload)
	if err != nil {
		t.Fatalf("DecodeBatch() error = %v", err)
	}
	if work.FirstChunkOrder != 7 || work.LastChunkOrder != 7 || work.ManifestPageCount != 2 || work.MaximumTokens != 800 || work.StructureVersion != profile.StructureVersion {
		t.Fatalf("DecodeBatch() work = %#v", work)
	}
}

func validManifestPayloads(t *testing.T) ([]byte, []byte) {
	t.Helper()
	source := sha256.Sum256([]byte("synthetic source"))
	processing := sha256.Sum256([]byte("processing profile"))
	generated := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	directory := "books/book-1/" + hex.EncodeToString(source[:]) + "/" + hex.EncodeToString(processing[:]) + "/"
	manifest := &ingestionv1.ChunkManifestV1{SchemaVersion: "v1", BookId: "book-1", SourceSha256: source[:], ProcessingConfigDigest: processing[:],
		ExtractionVersion: "poppler-layout-v1", NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1", ChunkingVersion: "token-window-v2",
		StructureVersion: "heading-carry-v1", MaximumTokens: 800, OverlapTokens: 120, PageCount: 1, ChunkCount: 1, GeneratedAt: timestamppb.New(generated),
		Shards: []*ingestionv1.ChunkShardDescriptorV1{{Reference: directory + "shards/000000.pb.zst", Sha256: source[:], CompressedByteSize: 10, UncompressedByteSize: 20, ChunkCount: 1, FirstChunkOrder: 0, LastChunkOrder: 0}}}
	manifestPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestPayload)
	outer := &ingestionv1.BookChunksReadyV1{EventId: "event-1", BookId: "book-1", SourceSha256: source[:], ManifestReference: directory + "manifest.pb", ManifestSha256: manifestDigest[:], ManifestByteSize: int64(len(manifestPayload)),
		PageCount: 1, ChunkCount: 1, ExtractionVersion: manifest.ExtractionVersion, NormalizationVersion: manifest.NormalizationVersion, TokenizerVersion: manifest.TokenizerVersion,
		ChunkingVersion: manifest.ChunkingVersion, StructureVersion: manifest.StructureVersion, MaximumTokens: manifest.MaximumTokens, OverlapTokens: manifest.OverlapTokens,
		CorrelationId: "correlation-1", OccurredAt: timestamppb.New(generated.Add(time.Minute)), CausationId: "cause-1", Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: "book-1:" + hex.EncodeToString(processing[:]) + ":ready"}
	eventPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return eventPayload, manifestPayload
}

func TestDecodeLifecycleEventsPreservesGenerationAndTrustedReferences(t *testing.T) {
	now := timestamppb.New(time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC))
	source := sha256.Sum256([]byte("source"))
	manifest := sha256.Sum256([]byte("manifest"))
	reindexPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&catalogv1.BookReindexRequestedV1{
		EventId: "reindex-event", BookId: "book-1", CommandId: "command-2", LifecycleVersion: 2,
		SourceSha256: source[:], ManifestReference: "books/book-1/source/profile/manifest.pb",
		ManifestSha256: manifest[:], ActorId: "actor-1", CorrelationId: "correlation-2",
		OccurredAt: now, CausationId: "command-2", Producer: "catalog-service", SchemaVersion: "v1",
		IdempotencyKey: "command-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	reindex, err := DecodeReindex(reindexPayload)
	if err != nil || reindex.LifecycleVersion != 2 || reindex.Kind != application.LifecycleReindex {
		t.Fatalf("DecodeReindex() = %#v, %v", reindex, err)
	}

	deletePayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&catalogv1.BookDeletionRequestedV1{
		EventId: "delete-event", BookId: "book-1", CommandId: "command-3", LifecycleVersion: 3,
		ActorId: "actor-1", CorrelationId: "correlation-3", OccurredAt: now, CausationId: "command-3",
		Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: "command-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := DecodeDeletion(deletePayload)
	if err != nil || deletion.LifecycleVersion != 3 || deletion.Kind != application.LifecycleDelete {
		t.Fatalf("DecodeDeletion() = %#v, %v", deletion, err)
	}
}

func TestDecodeLifecycleRejectsNegativeGeneration(t *testing.T) {
	payload, err := proto.Marshal(&catalogv1.BookDeletionRequestedV1{
		EventId: "delete-event", BookId: "book-1", CommandId: "command-3", LifecycleVersion: -1,
		ActorId: "actor-1", CorrelationId: "correlation-3", OccurredAt: timestamppb.Now(), CausationId: "command-3",
		Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: "command-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeDeletion(payload); !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("DecodeDeletion() error = %v", err)
	}
}
