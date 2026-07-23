// Package transport maps stable protobuf contracts to the application boundary.
package transport

import (
	"errors"
	"fmt"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	StartedRoute          = "ingestion.book.processing-started.v1"
	ReadyRoute            = "ingestion.book.chunks-ready.v1"
	FailedRoute           = "ingestion.book.processing-failed.v1"
	ArtifactsDeletedRoute = "ingestion.book.artifacts-deleted.v1"
)

func DecodeUploaded(payload []byte) (application.UploadedEvent, error) {
	if len(payload) == 0 || len(payload) > 256<<10 {
		return application.UploadedEvent{}, application.ErrInvalidEvent
	}
	var event catalogv1.BookUploadedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &event); err != nil || len(event.Sha256) != sha256Size || event.OccurredAt == nil || !event.OccurredAt.IsValid() {
		return application.UploadedEvent{}, application.ErrInvalidEvent
	}
	// Known v1 envelopes accept additive protobuf fields. The envelope remains
	// byte-bounded above and all authorization/security-relevant known fields are
	// still validated by UploadedEvent.Validate.
	var sum [sha256Size]byte
	copy(sum[:], event.Sha256)
	return application.UploadedEvent{EventID: event.EventId, BookID: event.BookId, ObjectReference: event.ObjectReference, MediaType: event.MediaType, CorrelationID: event.CorrelationId, CausationID: event.CausationId, Producer: event.Producer, SchemaVersion: event.SchemaVersion, IdempotencyKey: event.IdempotencyKey, SourceSHA256: sum, ByteSize: event.ByteSize, LifecycleVersion: event.LifecycleVersion, OccurredAt: event.OccurredAt.AsTime(), Payload: append([]byte(nil), payload...)}, nil
}

func DecodeDeletion(payload []byte) (application.DeletionEvent, error) {
	if len(payload) == 0 || len(payload) > 256<<10 {
		return application.DeletionEvent{}, application.ErrInvalidEvent
	}
	var event catalogv1.BookDeletionRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &event); err != nil ||
		event.OccurredAt == nil || !event.OccurredAt.IsValid() {
		return application.DeletionEvent{}, application.ErrInvalidEvent
	}
	decoded := application.DeletionEvent{
		EventID:          event.EventId,
		BookID:           event.BookId,
		CommandID:        event.CommandId,
		LifecycleVersion: event.LifecycleVersion,
		CorrelationID:    event.CorrelationId,
		CausationID:      event.CausationId,
		Producer:         event.Producer,
		SchemaVersion:    event.SchemaVersion,
		IdempotencyKey:   event.IdempotencyKey,
		OccurredAt:       event.OccurredAt.AsTime(),
		Payload:          append([]byte(nil), payload...),
	}
	if err := decoded.Validate(); err != nil {
		return application.DeletionEvent{}, err
	}
	return decoded, nil
}

const sha256Size = 32

type ProtoEventFactory struct{ newID application.IDGenerator }

func NewProtoEventFactory(newID application.IDGenerator) (*ProtoEventFactory, error) {
	if newID == nil {
		return nil, errors.New("event ID generator is required")
	}
	return &ProtoEventFactory{newID: newID}, nil
}

func (f *ProtoEventFactory) Started(source application.UploadedEvent, job domain.ProcessingJob, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	message := &ingestionv1.BookProcessingStartedV1{EventId: id, BookId: source.BookID, SourceSha256: source.SourceSHA256[:], ExtractionVersion: source.ExtractionVersion, NormalizationVersion: chunking.NormalizationVersion, TokenizerVersion: chunking.TokenizerVersion, ChunkingVersion: chunking.ChunkingVersion, CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID, Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: fmt.Sprintf("%s:%s:started", source.BookID, job.ConfigDigest()), LifecycleVersion: source.LifecycleVersion}
	return marshalOutbox(id, StartedRoute, now, message)
}

func (f *ProtoEventFactory) Ready(source application.UploadedEvent, job domain.ProcessingJob, result artifact.Result, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	message := &ingestionv1.BookChunksReadyV1{EventId: id, BookId: source.BookID, SourceSha256: source.SourceSHA256[:], ManifestReference: result.ManifestReference, ManifestSha256: result.ManifestSHA256[:], ManifestByteSize: result.ManifestByteSize, PageCount: result.PageCount, ChunkCount: result.ChunkCount, ExtractionVersion: source.ExtractionVersion, NormalizationVersion: chunking.NormalizationVersion, TokenizerVersion: chunking.TokenizerVersion, ChunkingVersion: chunking.ChunkingVersion, StructureVersion: chunking.StructureVersion, MaximumTokens: chunking.DefaultMaximumTokens, OverlapTokens: chunking.DefaultOverlapTokens, CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID, Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: fmt.Sprintf("%s:%s:ready", source.BookID, job.ConfigDigest()), LifecycleVersion: source.LifecycleVersion}
	return marshalOutbox(id, ReadyRoute, now, message)
}

func (f *ProtoEventFactory) Failed(source application.UploadedEvent, job domain.ProcessingJob, category domain.FailureCategory, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	protoCategory, ok := failureCategory(category)
	if !ok {
		return application.OutboxEvent{}, errors.New("invalid failure category")
	}
	message := &ingestionv1.BookProcessingFailedV1{EventId: id, BookId: source.BookID, SourceSha256: source.SourceSHA256[:], ExtractionVersion: source.ExtractionVersion, NormalizationVersion: chunking.NormalizationVersion, TokenizerVersion: chunking.TokenizerVersion, ChunkingVersion: chunking.ChunkingVersion, FailureCategory: protoCategory, CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID, Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: fmt.Sprintf("%s:%s:failed", source.BookID, job.ConfigDigest()), LifecycleVersion: source.LifecycleVersion}
	return marshalOutbox(id, FailedRoute, now, message)
}

func (f *ProtoEventFactory) ArtifactsDeleted(source application.DeletionEvent, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	message := &ingestionv1.BookArtifactsDeletedV1{
		EventId:          id,
		BookId:           source.BookID,
		CommandId:        source.CommandID,
		LifecycleVersion: source.LifecycleVersion,
		CorrelationId:    source.CorrelationID,
		OccurredAt:       timestamppb.New(now),
		CausationId:      source.EventID,
		Producer:         "ingestion-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   source.CommandID,
	}
	return marshalOutbox(id, ArtifactsDeletedRoute, now, message)
}

func marshalOutbox(id, eventType string, now time.Time, message proto.Message) (application.OutboxEvent, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return application.OutboxEvent{}, errors.New("encode event")
	}
	return application.OutboxEvent{ID: id, Type: eventType, Payload: payload, OccurredAt: now}, nil
}

func failureCategory(value domain.FailureCategory) (ingestionv1.BookProcessingFailureCategory, bool) {
	values := map[domain.FailureCategory]ingestionv1.BookProcessingFailureCategory{
		domain.FailureEncryptedDocument:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_ENCRYPTED_DOCUMENT,
		domain.FailureExtractionNotPermitted:  ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_EXTRACTION_NOT_PERMITTED,
		domain.FailureMalformedDocument:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_MALFORMED_DOCUMENT,
		domain.FailureUnsupportedDocument:     ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_UNSUPPORTED_DOCUMENT,
		domain.FailureNoExtractableText:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_NO_EXTRACTABLE_TEXT,
		domain.FailureResourceLimitExceeded:   ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_RESOURCE_LIMIT_EXCEEDED,
		domain.FailureSourceIntegrityMismatch: ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_SOURCE_INTEGRITY_MISMATCH,
		domain.FailureProcessingTimeout:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_PROCESSING_TIMEOUT,
		domain.FailureDependencyUnavailable:   ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_DEPENDENCY_UNAVAILABLE,
		domain.FailureInternalProcessing:      ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_INTERNAL_PROCESSING_ERROR,
	}
	result, ok := values[value]
	return result, ok
}
