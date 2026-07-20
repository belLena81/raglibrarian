package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
)

const (
	maxProcessingEventBytes = 64 << 10
	maxManifestBytes        = 1 << 20
	maxProcessedPages       = 500
	maxProcessedChunks      = 50_000
)

type processingProfile struct {
	extractionVersion    string
	normalizationVersion string
	tokenizerVersion     string
	chunkingVersion      string
	structureVersion     string
	maximumTokens        uint32
	overlapTokens        uint32
	configDigest         [sha256.Size]byte
}

var supportedM4Profile = newM4ProcessingProfile()

var supportedM5ProfileDigest = newM5ProfileDigest()

func newM5ProfileDigest() [sha256.Size]byte {
	values := []string{
		"jinaai/jina-embeddings-v2-base-code",
		"516f4baf13dec4ddddda8631e019b5737c8bc250",
		"768",
		"cosine",
		"mean",
		"normalized",
		"retrieval-index-v1",
		"poppler-layout-v1",
		"nfc-v1",
		"cl100k_base-v1",
		"token-window-v2",
		"heading-carry-v1",
		"800",
		"120",
		"v1",
	}
	return sha256.Sum256([]byte(strings.Join(values, "\x00") + "\x00"))
}

func newM4ProcessingProfile() processingProfile {
	// #nosec G101 -- token limits and tokenizer identifiers are public processing
	// contract values, not authentication credentials.
	profile := processingProfile{
		extractionVersion:    "poppler-layout-v1",
		normalizationVersion: "nfc-v1",
		tokenizerVersion:     "cl100k_base-v1",
		chunkingVersion:      "token-window-v2",
		structureVersion:     "heading-carry-v1",
		maximumTokens:        800,
		overlapTokens:        120,
	}
	// The final values are M4's maximum chunks, chunks per shard, and maximum
	// shard bytes. Future profiles need an explicit registry entry rather than
	// permissive acceptance on this v1 route.
	profile.configDigest = sha256.Sum256([]byte(strings.Join([]string{
		profile.extractionVersion,
		profile.normalizationVersion,
		profile.tokenizerVersion,
		profile.chunkingVersion,
		profile.structureVersion,
		fmt.Sprint(profile.maximumTokens),
		fmt.Sprint(profile.overlapTokens),
		"50000",
		"256",
		"4194304",
	}, "\x00")))
	return profile
}

var (
	ErrInvalidProcessingEvent  = errors.New("invalid processing event")
	ErrProcessingEventConflict = errors.New("processing event conflict")
)

// ProcessingEvent is the validated Catalog application input for one asynchronous processing fact.
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

// ProcessingEventRepository atomically deduplicates and applies a processing fact.
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
		if err := unmarshalStrict(payload, message); err != nil || !validProcessingProfile(message) {
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
		if err := unmarshalStrict(payload, message); err != nil || !validReadyDescriptor(message) {
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
		if err := unmarshalStrict(payload, message); err != nil || !validProcessingProfile(message) {
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
	case "retrieval.book.indexed.v1":
		message := &retrievalv1.BookIndexedV1{}
		if err := unmarshalStrict(payload, message); err != nil || !validIndexedDescriptor(message) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.EventID, event.BookID = message.GetEventId(), message.GetBookId()
		event.CorrelationID, event.CausationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
		occurredAt = timestampValue(message.GetOccurredAt())
		if !copyChecksum(&event.SourceSHA256, message.GetSourceSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.Fact.Kind = ProcessingIndexed
	case "retrieval.book.indexing-failed.v1":
		message := &retrievalv1.BookIndexingFailedV1{}
		if err := unmarshalStrict(payload, message); err != nil || !validIndexingFailedDescriptor(message) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.EventID, event.BookID = message.GetEventId(), message.GetBookId()
		event.CorrelationID, event.CausationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
		occurredAt = timestampValue(message.GetOccurredAt())
		if !copyChecksum(&event.SourceSHA256, message.GetSourceSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.Fact.Kind = ProcessingIndexingFailed
		event.Fact.FailureCategory = indexingFailureCategory(message.GetFailureCategory())
		if !validIndexingFailureCategory(event.Fact.FailureCategory) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
	default:
		return ProcessingEvent{}, ErrInvalidProcessingEvent
	}
	expectedProducer := "ingestion-service"
	if strings.HasPrefix(eventType, "retrieval.") {
		expectedProducer = "retrieval-service"
	}
	if !validEventIdentifier(event.EventID) || !validEventIdentifier(event.BookID) ||
		!validEventIdentifier(event.CorrelationID) || !validEventIdentifier(event.CausationID) ||
		!validEventIdentifier(idempotencyKey) || producer != expectedProducer || schemaVersion != "v1" || occurredAt.IsZero() {
		return ProcessingEvent{}, ErrInvalidProcessingEvent
	}
	return event, nil
}

func validIndexedDescriptor(message *retrievalv1.BookIndexedV1) bool {
	return message != nil && validEventIdentifier(message.GetJobId()) &&
		len(message.GetSourceSha256()) == sha256.Size && len(message.GetManifestSha256()) == sha256.Size &&
		bytes.Equal(message.GetIndexProfileDigest(), supportedM5ProfileDigest[:]) &&
		message.GetEvidenceCount() > 0 && message.GetEvidenceCount() <= maxProcessedChunks
}

func validIndexingFailedDescriptor(message *retrievalv1.BookIndexingFailedV1) bool {
	return message != nil && validEventIdentifier(message.GetJobId()) &&
		len(message.GetSourceSha256()) == sha256.Size && len(message.GetManifestSha256()) == sha256.Size &&
		bytes.Equal(message.GetIndexProfileDigest(), supportedM5ProfileDigest[:]) &&
		validIndexingFailureCategory(indexingFailureCategory(message.GetFailureCategory()))
}

type processingProfileDescriptor interface {
	GetExtractionVersion() string
	GetNormalizationVersion() string
	GetTokenizerVersion() string
	GetChunkingVersion() string
}

func validProcessingProfile(message processingProfileDescriptor) bool {
	return message.GetExtractionVersion() == supportedM4Profile.extractionVersion &&
		message.GetNormalizationVersion() == supportedM4Profile.normalizationVersion &&
		message.GetTokenizerVersion() == supportedM4Profile.tokenizerVersion &&
		message.GetChunkingVersion() == supportedM4Profile.chunkingVersion
}

func validReadyDescriptor(message *ingestionv1.BookChunksReadyV1) bool {
	if message == nil || !validEventIdentifier(message.GetBookId()) || len(message.GetSourceSha256()) != sha256.Size ||
		len(message.GetManifestSha256()) != sha256.Size || message.GetManifestByteSize() <= 0 || message.GetManifestByteSize() > maxManifestBytes ||
		message.GetPageCount() == 0 || message.GetPageCount() > maxProcessedPages ||
		message.GetChunkCount() == 0 || message.GetChunkCount() > maxProcessedChunks ||
		!validProcessingProfile(message) ||
		message.GetStructureVersion() != supportedM4Profile.structureVersion ||
		message.GetMaximumTokens() != supportedM4Profile.maximumTokens ||
		message.GetOverlapTokens() != supportedM4Profile.overlapTokens {
		return false
	}
	prefix := "books/" + message.GetBookId() + "/" + hex.EncodeToString(message.GetSourceSha256()) + "/"
	remainder, found := strings.CutPrefix(message.GetManifestReference(), prefix)
	return found && remainder == hex.EncodeToString(supportedM4Profile.configDigest[:])+"/manifest.pb"
}

func unmarshalStrict(payload []byte, message proto.Message) error {
	// The route and all known security-sensitive fields remain strictly
	// validated below. Discarding bounded unknown protobuf fields allows an
	// additive v1 producer rollout without breaking older Catalog consumers.
	return (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(payload, message)
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

func indexingFailureCategory(value retrievalv1.BookIndexingFailureCategory) ProcessingFailureCategory {
	switch value {
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_MANIFEST_INTEGRITY:
		return FailureManifestIntegrity
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INCOMPATIBLE_PROFILE:
		return FailureIncompatibleProfile
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_EMBEDDING_UNAVAILABLE:
		return FailureEmbeddingUnavailable
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_VECTOR_STORE_UNAVAILABLE:
		return FailureVectorStoreUnavailable
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_RESOURCE_LIMIT_EXCEEDED:
		return FailureResourceLimitExceeded
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INDEXING_TIMEOUT:
		return FailureIndexingTimeout
	case retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INTERNAL_INDEXING_ERROR:
		return FailureInternalIndexingError
	default:
		return ""
	}
}
