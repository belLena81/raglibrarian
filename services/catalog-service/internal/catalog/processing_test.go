package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
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

func TestSupportedM5ProfileDigestMatchesProducerContract(t *testing.T) {
	const expected = "7c986cd0d5eed17f398329c4a09edb7d7909309f1274e1b4ef1766397311681c"
	if digest := hex.EncodeToString(supportedM5ProfileDigest[:]); digest != expected {
		t.Fatalf("M5 profile digest = %q, want %q", digest, expected)
	}
}

func TestProcessingServiceValidatesRetrievalTerminalEvents(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	checksum := bytes.Repeat([]byte{1}, sha256.Size)
	manifestChecksum := bytes.Repeat([]byte{2}, sha256.Size)
	tests := []struct {
		name        string
		eventType   string
		message     proto.Message
		wantKind    ProcessingFactKind
		wantFailure ProcessingFailureCategory
	}{
		{
			name: "indexed", eventType: "retrieval.book.indexed.v1", wantKind: ProcessingIndexed,
			message: &retrievalv1.BookIndexedV1{
				EventId: "event-indexed", BookId: "book-1", JobId: "job-1", SourceSha256: checksum,
				ManifestSha256: manifestChecksum, IndexProfileDigest: supportedM5ProfileDigest[:], EvidenceCount: 3,
				CorrelationId: "correlation-1", CausationId: "batch-1", Producer: "retrieval-service",
				SchemaVersion: "v1", IdempotencyKey: "book-1:indexed:job-1", OccurredAt: timestamppb.New(now),
			},
		},
		{
			name: "failed", eventType: "retrieval.book.indexing-failed.v1", wantKind: ProcessingIndexingFailed,
			wantFailure: FailureEmbeddingUnavailable,
			message: &retrievalv1.BookIndexingFailedV1{
				EventId: "event-failed", BookId: "book-1", JobId: "job-1", SourceSha256: checksum,
				ManifestSha256: manifestChecksum, IndexProfileDigest: supportedM5ProfileDigest[:],
				FailureCategory: retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_EMBEDDING_UNAVAILABLE,
				CorrelationId:   "correlation-1", CausationId: "batch-1", Producer: "retrieval-service",
				SchemaVersion: "v1", IdempotencyKey: "book-1:failed:job-1", OccurredAt: timestamppb.New(now),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := proto.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			repository := &processingRepositoryFake{}
			service := NewProcessingService(repository, func() time.Time { return now.Add(time.Second) }, func() (string, error) { return "status-1", nil })
			changed, err := service.Handle(context.Background(), test.eventType, payload)
			if err != nil || !changed {
				t.Fatalf("Handle() = %v, %v", changed, err)
			}
			if repository.event.Fact.Kind != test.wantKind || repository.event.Fact.FailureCategory != test.wantFailure {
				t.Fatalf("fact = %+v", repository.event.Fact)
			}
		})
	}
}

func TestProcessingServiceRejectsUntrustedRetrievalTerminalEvents(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	valid := func() *retrievalv1.BookIndexedV1 {
		return &retrievalv1.BookIndexedV1{
			EventId: "event-indexed", BookId: "book-1", JobId: "job-1",
			SourceSha256: bytes.Repeat([]byte{1}, sha256.Size), ManifestSha256: bytes.Repeat([]byte{2}, sha256.Size),
			IndexProfileDigest: supportedM5ProfileDigest[:], EvidenceCount: 3,
			CorrelationId: "correlation-1", CausationId: "batch-1", Producer: "retrieval-service",
			SchemaVersion: "v1", IdempotencyKey: "book-1:indexed:job-1", OccurredAt: timestamppb.New(now),
		}
	}
	tests := []struct {
		name   string
		mutate func(*retrievalv1.BookIndexedV1)
	}{
		{name: "wrong producer", mutate: func(message *retrievalv1.BookIndexedV1) { message.Producer = "ingestion-service" }},
		{name: "wrong schema", mutate: func(message *retrievalv1.BookIndexedV1) { message.SchemaVersion = "v2" }},
		{name: "wrong profile", mutate: func(message *retrievalv1.BookIndexedV1) {
			message.IndexProfileDigest = bytes.Repeat([]byte{9}, sha256.Size)
		}},
		{name: "short profile", mutate: func(message *retrievalv1.BookIndexedV1) { message.IndexProfileDigest = []byte{1} }},
		{name: "short source checksum", mutate: func(message *retrievalv1.BookIndexedV1) { message.SourceSha256 = []byte{1} }},
		{name: "short manifest checksum", mutate: func(message *retrievalv1.BookIndexedV1) { message.ManifestSha256 = []byte{1} }},
		{name: "missing job", mutate: func(message *retrievalv1.BookIndexedV1) { message.JobId = "" }},
		{name: "empty evidence", mutate: func(message *retrievalv1.BookIndexedV1) { message.EvidenceCount = 0 }},
		{name: "too much evidence", mutate: func(message *retrievalv1.BookIndexedV1) { message.EvidenceCount = maxProcessedChunks + 1 }},
		{name: "invalid occurrence", mutate: func(message *retrievalv1.BookIndexedV1) { message.OccurredAt = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := valid()
			test.mutate(message)
			payload, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			repository := &processingRepositoryFake{}
			service := NewProcessingService(repository, func() time.Time { return now }, func() (string, error) { return "status-1", nil })
			changed, handleErr := service.Handle(context.Background(), "retrieval.book.indexed.v1", payload)
			if handleErr != ErrInvalidProcessingEvent || changed || repository.calls != 0 {
				t.Fatalf("Handle() = %v, %v; repository calls = %d", changed, handleErr, repository.calls)
			}
		})
	}
}

func TestIndexingFailureCategoryMappingIsClosedAndSanitized(t *testing.T) {
	tests := []struct {
		wire retrievalv1.BookIndexingFailureCategory
		want ProcessingFailureCategory
	}{
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_MANIFEST_INTEGRITY, FailureManifestIntegrity},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INCOMPATIBLE_PROFILE, FailureIncompatibleProfile},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_EMBEDDING_UNAVAILABLE, FailureEmbeddingUnavailable},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_VECTOR_STORE_UNAVAILABLE, FailureVectorStoreUnavailable},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_RESOURCE_LIMIT_EXCEEDED, FailureResourceLimitExceeded},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INDEXING_TIMEOUT, FailureIndexingTimeout},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INTERNAL_INDEXING_ERROR, FailureInternalIndexingError},
		{retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_UNSPECIFIED, ""},
		{retrievalv1.BookIndexingFailureCategory(99), ""},
	}
	for _, test := range tests {
		if got := indexingFailureCategory(test.wire); got != test.want {
			t.Fatalf("indexingFailureCategory(%d) = %q, want %q", test.wire, got, test.want)
		}
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

func TestProcessingServiceRejectsUnsupportedCommonProfilesBeforePersistence(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		mutate func(*processingProfileFields)
	}{
		{name: "missing extraction", mutate: func(profile *processingProfileFields) { profile.extractionVersion = "" }},
		{name: "changed extraction", mutate: func(profile *processingProfileFields) { profile.extractionVersion = "poppler-layout-v2" }},
		{name: "missing normalization", mutate: func(profile *processingProfileFields) { profile.normalizationVersion = "" }},
		{name: "changed normalization", mutate: func(profile *processingProfileFields) { profile.normalizationVersion = "nfc-v2" }},
		{name: "missing tokenizer", mutate: func(profile *processingProfileFields) { profile.tokenizerVersion = "" }},
		{name: "changed tokenizer", mutate: func(profile *processingProfileFields) { profile.tokenizerVersion = "cl100k-base-v1" }},
		{name: "missing chunking", mutate: func(profile *processingProfileFields) { profile.chunkingVersion = "" }},
		{name: "changed chunking", mutate: func(profile *processingProfileFields) { profile.chunkingVersion = "token-window-v3" }},
	}

	for _, eventType := range []string{
		"ingestion.book.processing-started.v1",
		"ingestion.book.processing-failed.v1",
	} {
		for _, test := range tests {
			t.Run(eventType+"/"+test.name, func(t *testing.T) {
				profile := processingProfileFields{
					extractionVersion:    supportedM4Profile.extractionVersion,
					normalizationVersion: supportedM4Profile.normalizationVersion,
					tokenizerVersion:     supportedM4Profile.tokenizerVersion,
					chunkingVersion:      supportedM4Profile.chunkingVersion,
				}
				test.mutate(&profile)
				payload := processingEventPayload(t, eventType, now, profile)
				repository := &processingRepositoryFake{}
				service := NewProcessingService(repository, func() time.Time { return now }, func() (string, error) { return "status-1", nil })

				changed, err := service.Handle(context.Background(), eventType, payload)

				if err != ErrInvalidProcessingEvent || changed {
					t.Fatalf("Handle() = %v, %v", changed, err)
				}
				if repository.calls != 0 {
					t.Fatalf("repository calls = %d, want 0", repository.calls)
				}
			})
		}
	}
}

type processingProfileFields struct {
	extractionVersion    string
	normalizationVersion string
	tokenizerVersion     string
	chunkingVersion      string
}

func processingEventPayload(t *testing.T, eventType string, occurredAt time.Time, profile processingProfileFields) []byte {
	t.Helper()
	commonChecksum := bytes.Repeat([]byte{1}, sha256.Size)
	var message proto.Message
	switch eventType {
	case "ingestion.book.processing-started.v1":
		message = &ingestionv1.BookProcessingStartedV1{
			EventId: "event-1", BookId: "book-1", SourceSha256: commonChecksum,
			ExtractionVersion: profile.extractionVersion, NormalizationVersion: profile.normalizationVersion,
			TokenizerVersion: profile.tokenizerVersion, ChunkingVersion: profile.chunkingVersion,
			CorrelationId: "correlation-1", CausationId: "upload-1", Producer: "ingestion-service",
			SchemaVersion: "v1", IdempotencyKey: "book-1:v1", OccurredAt: timestamppb.New(occurredAt),
		}
	case "ingestion.book.processing-failed.v1":
		message = &ingestionv1.BookProcessingFailedV1{
			EventId: "event-1", BookId: "book-1", SourceSha256: commonChecksum,
			ExtractionVersion: profile.extractionVersion, NormalizationVersion: profile.normalizationVersion,
			TokenizerVersion: profile.tokenizerVersion, ChunkingVersion: profile.chunkingVersion,
			FailureCategory: ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_INTERNAL_PROCESSING_ERROR,
			CorrelationId:   "correlation-1", CausationId: "upload-1", Producer: "ingestion-service",
			SchemaVersion: "v1", IdempotencyKey: "book-1:v1", OccurredAt: timestamppb.New(occurredAt),
		}
	default:
		t.Fatalf("unsupported test event type %q", eventType)
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
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
		ExtractionVersion: supportedM4Profile.extractionVersion, NormalizationVersion: supportedM4Profile.normalizationVersion,
		TokenizerVersion: supportedM4Profile.tokenizerVersion, ChunkingVersion: supportedM4Profile.chunkingVersion,
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
	calls         int
}

func (r *processingRepositoryFake) ApplyProcessingEvent(_ context.Context, event ProcessingEvent, statusEventID string, _ time.Time) (Book, bool, error) {
	r.calls++
	r.event = event
	r.statusEventID = statusEventID
	return Book{}, true, nil
}
