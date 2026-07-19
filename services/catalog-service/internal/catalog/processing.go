package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
)

const maxProcessingEventBytes = 64 << 10

var (
	ErrInvalidProcessingEvent  = errors.New("invalid processing event")
	ErrProcessingEventConflict = errors.New("processing event conflict")
)

// ProcessingEvent is the validated Catalog application input for one Ingestion fact.
type ProcessingEvent struct {
	EventID       string
	EventType     string
	BookID        string
	SourceSHA256  [sha256.Size]byte
	PayloadSHA256 [sha256.Size]byte
	CorrelationID string
	CausationID   string
	Fact          ProcessingFact
}

// ProcessingEventRepository atomically deduplicates and applies an Ingestion fact.
type ProcessingEventRepository interface {
	ApplyProcessingEvent(context.Context, ProcessingEvent, string, time.Time) (Book, bool, error)
}

// ProcessingService validates versioned events before they reach persistence.
type ProcessingService struct {
	repository ProcessingEventRepository
	now        func() time.Time
	newID      func() (string, error)
}

func NewProcessingService(repository ProcessingEventRepository, now func() time.Time, newID func() (string, error)) *ProcessingService {
	if repository == nil {
		panic("catalog: processing repository is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if newID == nil {
		newID = generatedID
	}
	return &ProcessingService{repository: repository, now: now, newID: newID}
}

func (s *ProcessingService) Handle(ctx context.Context, eventType string, payload []byte) (bool, error) {
	return s.handle(ctx, eventType, "", payload)
}

// HandleEnvelope additionally binds the trusted AMQP envelope to its protobuf payload.
func (s *ProcessingService) HandleEnvelope(ctx context.Context, eventType, messageID string, payload []byte) (bool, error) {
	if !validEventIdentifier(messageID) {
		return false, ErrInvalidProcessingEvent
	}
	return s.handle(ctx, eventType, messageID, payload)
}

func (s *ProcessingService) handle(ctx context.Context, eventType, messageID string, payload []byte) (bool, error) {
	event, err := decodeProcessingEvent(eventType, payload)
	if err != nil {
		return false, err
	}
	if messageID != "" && event.EventID != messageID {
		return false, ErrInvalidProcessingEvent
	}
	event.Fact.OccurredAt = s.now().UTC()
	statusEventID, err := s.newID()
	if err != nil {
		return false, fmt.Errorf("generate status event ID: %w", err)
	}
	_, changed, err := s.repository.ApplyProcessingEvent(ctx, event, statusEventID, event.Fact.OccurredAt)
	return changed, err
}

func decodeProcessingEvent(eventType string, payload []byte) (ProcessingEvent, error) {
	if len(payload) == 0 || len(payload) > maxProcessingEventBytes {
		return ProcessingEvent{}, ErrInvalidProcessingEvent
	}
	event := ProcessingEvent{EventType: eventType, PayloadSHA256: sha256.Sum256(payload)}
	var producer, schemaVersion, idempotencyKey string
	var occurredAt time.Time
	switch eventType {
	case "ingestion.book.processing-started.v1":
		message := &ingestionv1.BookProcessingStartedV1{}
		if err := unmarshalStrict(payload, message); err != nil {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.EventID, event.BookID = message.GetEventId(), message.GetBookId()
		event.CorrelationID, event.CausationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
		occurredAt = timestampValue(message.GetOccurredAt())
		if !copyChecksum(&event.SourceSHA256, message.GetSourceSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.Fact.Kind = ProcessingStarted
	case "ingestion.book.chunks-ready.v1":
		message := &ingestionv1.BookChunksReadyV1{}
		if err := unmarshalStrict(payload, message); err != nil || message.GetManifestReference() == "" || len(message.GetManifestSha256()) != sha256.Size || message.GetManifestByteSize() <= 0 || message.GetPageCount() == 0 || message.GetChunkCount() == 0 {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.EventID, event.BookID = message.GetEventId(), message.GetBookId()
		event.CorrelationID, event.CausationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
		occurredAt = timestampValue(message.GetOccurredAt())
		if !copyChecksum(&event.SourceSHA256, message.GetSourceSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.Fact.Kind = ProcessingChunksReady
	case "ingestion.book.processing-failed.v1":
		message := &ingestionv1.BookProcessingFailedV1{}
		if err := unmarshalStrict(payload, message); err != nil {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.EventID, event.BookID = message.GetEventId(), message.GetBookId()
		event.CorrelationID, event.CausationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
		occurredAt = timestampValue(message.GetOccurredAt())
		if !copyChecksum(&event.SourceSHA256, message.GetSourceSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.Fact.Kind = ProcessingFailed
		event.Fact.FailureCategory = failureCategory(message.GetFailureCategory())
		if !validFailureCategory(event.Fact.FailureCategory) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
	default:
		return ProcessingEvent{}, ErrInvalidProcessingEvent
	}
	if !validEventIdentifier(event.EventID) || !validEventIdentifier(event.BookID) ||
		!validEventIdentifier(event.CorrelationID) || !validEventIdentifier(event.CausationID) ||
		!validEventIdentifier(idempotencyKey) || producer != "ingestion-service" || schemaVersion != "v1" || occurredAt.IsZero() {
		return ProcessingEvent{}, ErrInvalidProcessingEvent
	}
	return event, nil
}

func unmarshalStrict(payload []byte, message proto.Message) error {
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, message); err != nil {
		return err
	}
	if len(message.ProtoReflect().GetUnknown()) != 0 {
		return ErrInvalidProcessingEvent
	}
	return nil
}

func validEventIdentifier(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func timestampValue(value interface {
	IsValid() bool
	AsTime() time.Time
}) time.Time {
	if value == nil || !value.IsValid() {
		return time.Time{}
	}
	return value.AsTime().UTC()
}

func copyChecksum(target *[sha256.Size]byte, value []byte) bool {
	if len(value) != sha256.Size {
		return false
	}
	copy(target[:], value)
	return true
}

func failureCategory(value ingestionv1.BookProcessingFailureCategory) ProcessingFailureCategory {
	switch value {
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_ENCRYPTED_DOCUMENT:
		return FailureEncryptedDocument
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_EXTRACTION_NOT_PERMITTED:
		return FailureExtractionNotPermitted
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_MALFORMED_DOCUMENT:
		return FailureMalformedDocument
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_UNSUPPORTED_DOCUMENT:
		return FailureUnsupportedDocument
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_NO_EXTRACTABLE_TEXT:
		return FailureNoExtractableText
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_RESOURCE_LIMIT_EXCEEDED:
		return FailureResourceLimitExceeded
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_SOURCE_INTEGRITY_MISMATCH:
		return FailureSourceIntegrityMismatch
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_PROCESSING_TIMEOUT:
		return FailureProcessingTimeout
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_DEPENDENCY_UNAVAILABLE:
		return FailureDependencyUnavailable
	case ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_INTERNAL_PROCESSING_ERROR:
		return FailureInternalProcessingError
	default:
		return ""
	}
}
