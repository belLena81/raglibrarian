package catalog

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
)

func TestProcessingServiceValidatesAndAppliesReadyEvent(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	checksum := bytes.Repeat([]byte{1}, 32)
	manifestReference := validM4ManifestReference("book-1", checksum)
	payload, err := proto.Marshal(&ingestionv1.BookChunksReadyV1{
		EventId: "event-1", BookId: "book-1", SourceSha256: checksum,
		ManifestReference: manifestReference, ManifestSha256: checksum, ManifestByteSize: 128,
		PageCount: 2, ChunkCount: 3, CorrelationId: "correlation-1", CausationId: "upload-1",
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: "book-1:v1",
		ExtractionVersion: supportedM4Profile.extractionVersion, NormalizationVersion: supportedM4Profile.normalizationVersion, TokenizerVersion: supportedM4Profile.tokenizerVersion,
		ChunkingVersion: supportedM4Profile.chunkingVersion, StructureVersion: supportedM4Profile.structureVersion, MaximumTokens: supportedM4Profile.maximumTokens, OverlapTokens: supportedM4Profile.overlapTokens,
		OccurredAt: timestamppb.New(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &processingRepositoryFake{}
	service := NewProcessingService(repository, func() time.Time { return now.Add(time.Second) }, func() (string, error) { return "status-1", nil })
	changed, err := service.Handle(context.Background(), "ingestion.book.chunks-ready.v1", payload)
	if err != nil || !changed {
		t.Fatalf("Handle() = %v, %v", changed, err)
	}
	if repository.event.BookID != "book-1" || repository.event.Fact.Kind != ProcessingChunksReady || repository.statusEventID != "status-1" {
		t.Fatalf("event = %+v, status ID = %q", repository.event, repository.statusEventID)
	}
}

func TestSupportedM4ProfileDigestMatchesProducerContract(t *testing.T) {
	const expected = "bf78af147282f437086fe289afc14968ef7e20db0546c63672369e6530a18add"
	if digest := hex.EncodeToString(supportedM4Profile.configDigest[:]); digest != expected {
		t.Fatalf("M4 config digest = %q, want %q", digest, expected)
	}
}

func TestProcessingServiceRejectsUntrustedReadyDescriptors(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	sourceChecksum := bytes.Repeat([]byte{1}, 32)
	configDigest := hex.EncodeToString(supportedM4Profile.configDigest[:])
	validReference := validM4ManifestReference("book-1", sourceChecksum)

	tests := []struct {
		name   string
		mutate func(*ingestionv1.BookChunksReadyV1)
	}{
		{name: "traversal", mutate: func(message *ingestionv1.BookChunksReadyV1) {
			message.ManifestReference = "books/book-1/../manifest.pb"
		}},
		{name: "wrong book", mutate: func(message *ingestionv1.BookChunksReadyV1) {
			message.ManifestReference = "books/book-2/" + hex.EncodeToString(sourceChecksum) + "/" + configDigest + "/manifest.pb"
		}},
		{name: "wrong source", mutate: func(message *ingestionv1.BookChunksReadyV1) {
			message.ManifestReference = "books/book-1/" + string(bytes.Repeat([]byte{'3'}, 64)) + "/" + configDigest + "/manifest.pb"
		}},
		{name: "uppercase config digest", mutate: func(message *ingestionv1.BookChunksReadyV1) {
			message.ManifestReference = "books/book-1/" + hex.EncodeToString(sourceChecksum) + "/" + string(bytes.Repeat([]byte{'A'}, 64)) + "/manifest.pb"
		}},
		{name: "unsupported config digest", mutate: func(message *ingestionv1.BookChunksReadyV1) {
			message.ManifestReference = "books/book-1/" + hex.EncodeToString(sourceChecksum) + "/" + string(bytes.Repeat([]byte{'2'}, 64)) + "/manifest.pb"
		}},
		{name: "changed extraction", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.ExtractionVersion = "poppler-layout-v2" }},
		{name: "changed normalization", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.NormalizationVersion = "nfc-v2" }},
		{name: "changed tokenizer", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.TokenizerVersion = "cl100k-base-v1" }},
		{name: "changed chunking", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.ChunkingVersion = "token-window-v3" }},
		{name: "unknown structure", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.StructureVersion = "future-v2" }},
		{name: "missing extraction version", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.ExtractionVersion = "" }},
		{name: "changed overlap", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.OverlapTokens = 119 }},
		{name: "changed token window", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.MaximumTokens = 799 }},
		{name: "too many pages", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.PageCount = maxProcessedPages + 1 }},
		{name: "too many chunks", mutate: func(message *ingestionv1.BookChunksReadyV1) { message.ChunkCount = maxProcessedChunks + 1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := &ingestionv1.BookChunksReadyV1{
				EventId: "event-1", BookId: "book-1", SourceSha256: sourceChecksum,
				ManifestReference: validReference, ManifestSha256: sourceChecksum, ManifestByteSize: 128,
				PageCount: 2, ChunkCount: 3, CorrelationId: "correlation-1", CausationId: "upload-1",
				Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: "book-1:v1",
				ExtractionVersion: supportedM4Profile.extractionVersion, NormalizationVersion: supportedM4Profile.normalizationVersion, TokenizerVersion: supportedM4Profile.tokenizerVersion,
				ChunkingVersion: supportedM4Profile.chunkingVersion, StructureVersion: supportedM4Profile.structureVersion, MaximumTokens: supportedM4Profile.maximumTokens, OverlapTokens: supportedM4Profile.overlapTokens,
				OccurredAt: timestamppb.New(now),
			}
			test.mutate(message)
			payload, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			service := NewProcessingService(&processingRepositoryFake{}, func() time.Time { return now }, func() (string, error) { return "status-1", nil })
			if changed, handleErr := service.Handle(context.Background(), "ingestion.book.chunks-ready.v1", payload); handleErr != ErrInvalidProcessingEvent || changed {
				t.Fatalf("Handle() = %v, %v", changed, handleErr)
			}
		})
	}
}

func validM4ManifestReference(bookID string, sourceChecksum []byte) string {
	return "books/" + bookID + "/" + hex.EncodeToString(sourceChecksum) + "/" + hex.EncodeToString(supportedM4Profile.configDigest[:]) + "/manifest.pb"
}

func TestProcessingServiceRejectsUnknownAndMalformedEvents(t *testing.T) {
	service := NewProcessingService(&processingRepositoryFake{}, time.Now, func() (string, error) { return "id", nil })
	for _, test := range []struct {
		typeName string
		payload  []byte
	}{
		{typeName: "unknown.v1", payload: []byte{1}},
		{typeName: "ingestion.book.processing-started.v1", payload: []byte("not protobuf")},
	} {
		if _, err := service.Handle(context.Background(), test.typeName, test.payload); err != ErrInvalidProcessingEvent {
			t.Fatalf("Handle(%q) error = %v", test.typeName, err)
		}
	}
}

func TestProcessingServiceAcceptsAdditiveUnknownFieldsOnKnownV1Route(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	payload, err := proto.Marshal(&ingestionv1.BookProcessingStartedV1{
		EventId: "event-1", BookId: "book-1", SourceSha256: bytes.Repeat([]byte{1}, 32),
		CorrelationId: "correlation-1", CausationId: "upload-1", Producer: "ingestion-service",
		SchemaVersion: "v1", IdempotencyKey: "book-1:v1", OccurredAt: timestamppb.New(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Field 99, wire type varint, value 1 emulates a future additive field.
	payload = append(payload, 0x98, 0x06, 0x01)
	repository := &processingRepositoryFake{}
	service := NewProcessingService(repository, func() time.Time { return now }, func() (string, error) { return "status-1", nil })
	if changed, handleErr := service.Handle(context.Background(), "ingestion.book.processing-started.v1", payload); handleErr != nil || !changed {
		t.Fatalf("Handle() = %v, %v", changed, handleErr)
	}
}

type processingRepositoryFake struct {
	event         ProcessingEvent
	statusEventID string
}

func (r *processingRepositoryFake) ApplyProcessingEvent(_ context.Context, event ProcessingEvent, statusEventID string, _ time.Time) (Book, bool, error) {
	r.event = event
	r.statusEventID = statusEventID
	return Book{}, true, nil
}
