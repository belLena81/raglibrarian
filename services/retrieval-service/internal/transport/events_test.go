package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
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
