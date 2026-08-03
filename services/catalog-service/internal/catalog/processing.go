package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/belLena81/raglibrarian/pkg/indexprofile"
	"google.golang.org/protobuf/proto"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
)

const (
	maxProcessedPages  = 1000
	maxProcessedChunks = 50_000
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
	configDigests        [][sha256.Size]byte
}

var supportedM4Profile = newProcessingProfile(indexprofile.ExtractionPDF)
var supportedM7EPUBProfile = newProcessingProfile(indexprofile.ExtractionEPUB)

// The filtered profile digests bind Catalog to every currently supported
// content-selection mode for the v1 selector profile. A producer configuration
// change requires an explicit Catalog registry update, so unreviewed manifest
// contracts fail closed.
var supportedM8PDFFilteredProfile = newProcessingProfileWithContentSelection(
	indexprofile.ExtractionPDFFiltered,
	indexprofile.ContentSelectionModeEnforcement,
	indexprofile.ContentSelectionModeObservation,
)
var supportedM8EPUBFilteredProfile = newProcessingProfileWithContentSelection(
	indexprofile.ExtractionEPUBFiltered,
	indexprofile.ContentSelectionModeEnforcement,
	indexprofile.ContentSelectionModeObservation,
)

var supportedM5ProfileDigest = newM5ProfileDigest(indexprofile.ExtractionPDF)
var supportedM7EPUBIndexProfileDigest = newM5ProfileDigest(indexprofile.ExtractionEPUB)
var supportedM8PDFFilteredIndexProfileDigest = newM5ProfileDigest(indexprofile.ExtractionPDFFiltered)
var supportedM8EPUBFilteredIndexProfileDigest = newM5ProfileDigest(indexprofile.ExtractionEPUBFiltered)

func newM5ProfileDigest(extractionVersion string) [sha256.Size]byte {
	parts := []string{
		indexprofile.EmbeddingModel,
		indexprofile.EmbeddingRevision,
		strconv.Itoa(indexprofile.EmbeddingDimensions),
		indexprofile.DistanceCosine,
		indexprofile.PoolingCLS,
		indexprofile.NormalizationNormalized,
		indexprofile.IndexSchema,
		extractionVersion,
		indexprofile.NormalizationNFC,
		indexprofile.TokenizerCL100K,
		indexprofile.ChunkingChapterPageWindow,
		indexprofile.StructureChapterBoundary,
		strconv.Itoa(indexprofile.MaximumTokens),
		strconv.Itoa(indexprofile.OverlapTokens),
		indexprofile.ManifestSchema,
	}
	if extractionVersion == indexprofile.ExtractionPDFFiltered || extractionVersion == indexprofile.ExtractionEPUBFiltered {
		parts = append(parts, indexprofile.ContentSelectionV1)
	}
	return indexprofile.Digest(parts...)
}

func newProcessingProfile(extractionVersion string) processingProfile {
	// #nosec G101 -- token limits and tokenizer identifiers are public processing
	// contract values, not authentication credentials.
	profile := processingProfile{
		extractionVersion:    extractionVersion,
		normalizationVersion: indexprofile.NormalizationNFC,
		tokenizerVersion:     indexprofile.TokenizerCL100K,
		chunkingVersion:      indexprofile.ChunkingChapterPageWindow,
		structureVersion:     indexprofile.StructureChapterBoundary,
		maximumTokens:        indexprofile.MaximumTokens,
		overlapTokens:        indexprofile.OverlapTokens,
	}
	profile.configDigest = processingConfigDigest(extractionVersion, nil)
	profile.configDigests = [][sha256.Size]byte{profile.configDigest}
	return profile
}

func newProcessingProfileWithContentSelection(extractionVersion string, modes ...indexprofile.ContentSelectionMode) processingProfile {
	profile := newProcessingProfile(extractionVersion)
	profile.configDigests = make([][sha256.Size]byte, 0, len(modes))
	for _, mode := range modes {
		contentSelection := supportedContentSelectionProfile(mode)
		profile.configDigests = append(profile.configDigests, processingConfigDigest(extractionVersion, &contentSelection))
	}
	if len(profile.configDigests) == 0 {
		panic("catalog: content-selection profile requires at least one digest")
	}
	profile.configDigest = profile.configDigests[0]
	return profile
}

func processingConfigDigest(extractionVersion string, selection *indexprofile.ContentSelectionProfile) [sha256.Size]byte {
	return (indexprofile.ProcessingConfigProfile{
		ExtractionVersion:    extractionVersion,
		NormalizationVersion: indexprofile.NormalizationNFC,
		TokenizerVersion:     indexprofile.TokenizerCL100K,
		ChunkingVersion:      indexprofile.ChunkingChapterPageWindow,
		StructureVersion:     indexprofile.StructureChapterBoundary,
		MaximumTokens:        indexprofile.MaximumTokens,
		OverlapTokens:        indexprofile.OverlapTokens,
		TargetPages:          2,
		MaximumPages:         3,
		MaximumChunks:        50_000,
		ChunksPerShard:       256,
		MaximumShardBytes:    4 << 20,
		ContentSelection:     selection,
	}).Digest()
}

func supportedContentSelectionProfile(mode indexprofile.ContentSelectionMode) indexprofile.ContentSelectionProfile {
	return indexprofile.ContentSelectionProfile{
		Mode:                 mode,
		PolicyVersion:        indexprofile.ContentSelectionV1,
		ParserVersion:        indexprofile.ContentSelectionParserBBoxLayoutV1,
		ModelSHA256:          indexprofile.ContentSelectionModelSHA256,
		MinimumSignals:       indexprofile.ContentSelectionMinimumSignals,
		MaximumRanges:        indexprofile.ContentSelectionMaximumRanges,
		MaximumExcludedRatio: indexprofile.ContentSelectionMaximumExcludedRatio,
	}
}

var (
	ErrInvalidProcessingEvent  = errors.New("invalid processing event")
	ErrProcessingEventConflict = errors.New("processing event conflict")
)

// ProcessingEvent is the validated Catalog application input for one asynchronous processing fact.
type ProcessingEvent struct {
	EventID           string
	EventType         string
	BookID            string
	SourceSHA256      [sha256.Size]byte
	PayloadSHA256     [sha256.Size]byte
	CorrelationID     string
	CausationID       string
	LifecycleVersion  int64
	ManifestReference string
	ManifestSHA256    [sha256.Size]byte
	Fact              ProcessingFact
}

// ProcessingEventRepository atomically deduplicates and applies a processing fact.
type ProcessingEventRepository interface {
	ApplyProcessingEvent(context.Context, ProcessingEvent, string, time.Time) (Book, bool, error)
}

type LifecycleAck struct {
	EventID          string
	EventType        string
	BookID           string
	CommandID        string
	LifecycleVersion int64
	PayloadSHA256    [sha256.Size]byte
	OccurredAt       time.Time
}

type LifecycleAckRepository interface {
	ApplyLifecycleAck(context.Context, LifecycleAck, time.Time) (Book, bool, error)
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
	if eventType == "ingestion.book.artifacts-deleted.v1" || eventType == "retrieval.book.index-deleted.v1" {
		ack, err := decodeLifecycleAck(eventType, payload)
		if err != nil || (messageID != "" && ack.EventID != messageID) {
			return false, ErrInvalidProcessingEvent
		}
		repository, ok := s.repository.(LifecycleAckRepository)
		if !ok {
			return false, errors.New("catalog lifecycle acknowledgement persistence unavailable")
		}
		_, changed, err := repository.ApplyLifecycleAck(ctx, ack, s.now().UTC())
		return changed, err
	}
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

func decodeLifecycleAck(eventType string, payload []byte) (LifecycleAck, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return LifecycleAck{}, ErrInvalidProcessingEvent
	}
	ack := LifecycleAck{EventType: eventType, PayloadSHA256: sha256.Sum256(payload)}
	var correlationID, causationID, producer, schemaVersion, idempotencyKey string
	switch eventType {
	case "ingestion.book.artifacts-deleted.v1":
		message := &ingestionv1.BookArtifactsDeletedV1{}
		if err := unmarshalStrict(payload, message); err != nil {
			return LifecycleAck{}, ErrInvalidProcessingEvent
		}
		ack.EventID, ack.BookID, ack.CommandID = message.GetEventId(), message.GetBookId(), message.GetCommandId()
		ack.LifecycleVersion, ack.OccurredAt = message.GetLifecycleVersion(), timestampValue(message.GetOccurredAt())
		correlationID, causationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
	case "retrieval.book.index-deleted.v1":
		message := &retrievalv1.BookIndexDeletedV1{}
		if err := unmarshalStrict(payload, message); err != nil {
			return LifecycleAck{}, ErrInvalidProcessingEvent
		}
		ack.EventID, ack.BookID, ack.CommandID = message.GetEventId(), message.GetBookId(), message.GetCommandId()
		ack.LifecycleVersion, ack.OccurredAt = message.GetLifecycleVersion(), timestampValue(message.GetOccurredAt())
		correlationID, causationID = message.GetCorrelationId(), message.GetCausationId()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
	default:
		return LifecycleAck{}, ErrInvalidProcessingEvent
	}
	expectedProducer := "ingestion-service"
	if strings.HasPrefix(eventType, "retrieval.") {
		expectedProducer = "retrieval-service"
	}
	if !validEventIdentifier(ack.EventID) || !validEventIdentifier(ack.BookID) ||
		!validEventIdentifier(ack.CommandID) || !validEventIdentifier(correlationID) ||
		!validEventIdentifier(causationID) || !validEventIdentifier(idempotencyKey) ||
		ack.LifecycleVersion < 1 || ack.OccurredAt.IsZero() ||
		producer != expectedProducer || schemaVersion != "v1" {
		return LifecycleAck{}, ErrInvalidProcessingEvent
	}
	return ack, nil
}

func decodeProcessingEvent(eventType string, payload []byte) (ProcessingEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
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
		event.LifecycleVersion = message.GetLifecycleVersion()
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
		event.LifecycleVersion = message.GetLifecycleVersion()
		event.ManifestReference = message.GetManifestReference()
		if !copyChecksum(&event.ManifestSHA256, message.GetManifestSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
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
		event.LifecycleVersion = message.GetLifecycleVersion()
		producer, schemaVersion, idempotencyKey = message.GetProducer(), message.GetSchemaVersion(), message.GetIdempotencyKey()
		occurredAt = timestampValue(message.GetOccurredAt())
		if !copyChecksum(&event.SourceSHA256, message.GetSourceSha256()) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.Fact.Kind = ProcessingFailed
		event.Fact.FailureCategory = failureCategory(message.GetFailureCategory())
		event.Fact.FailureDetail = message.GetFailureDetail()
		if !validFailureCategory(event.Fact.FailureCategory) || !validFailureDetail(event.Fact.FailureDetail) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
	case "retrieval.book.indexed.v1":
		message := &retrievalv1.BookIndexedV1{}
		if err := unmarshalStrict(payload, message); err != nil || !validIndexedDescriptor(message) {
			return ProcessingEvent{}, ErrInvalidProcessingEvent
		}
		event.EventID, event.BookID = message.GetEventId(), message.GetBookId()
		event.CorrelationID, event.CausationID = message.GetCorrelationId(), message.GetCausationId()
		event.LifecycleVersion = message.GetLifecycleVersion()
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
		event.LifecycleVersion = message.GetLifecycleVersion()
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
	// Zero is accepted during the additive rollout and maps to the original
	// generation. New producers always send an explicit positive version.
	if event.LifecycleVersion < 0 {
		return ProcessingEvent{}, ErrInvalidProcessingEvent
	}
	if event.LifecycleVersion == 0 {
		event.LifecycleVersion = 1
	}
	return event, nil
}

func validIndexedDescriptor(message *retrievalv1.BookIndexedV1) bool {
	return message != nil && validEventIdentifier(message.GetJobId()) &&
		len(message.GetSourceSha256()) == sha256.Size && len(message.GetManifestSha256()) == sha256.Size &&
		supportedIndexProfileDigest(message.GetIndexProfileDigest()) &&
		message.GetEvidenceCount() > 0 && message.GetEvidenceCount() <= maxProcessedChunks
}

func validIndexingFailedDescriptor(message *retrievalv1.BookIndexingFailedV1) bool {
	return message != nil && validEventIdentifier(message.GetJobId()) &&
		len(message.GetSourceSha256()) == sha256.Size && len(message.GetManifestSha256()) == sha256.Size &&
		supportedIndexProfileDigest(message.GetIndexProfileDigest()) &&
		validIndexingFailureCategory(indexingFailureCategory(message.GetFailureCategory()))
}

type processingProfileDescriptor interface {
	GetExtractionVersion() string
	GetNormalizationVersion() string
	GetTokenizerVersion() string
	GetChunkingVersion() string
}

func validProcessingProfile(message processingProfileDescriptor) bool {
	profile, ok := processingProfileForExtraction(message.GetExtractionVersion())
	return ok && message.GetNormalizationVersion() == profile.normalizationVersion &&
		message.GetTokenizerVersion() == profile.tokenizerVersion &&
		message.GetChunkingVersion() == profile.chunkingVersion
}

func validReadyDescriptor(message *ingestionv1.BookChunksReadyV1) bool {
	profile, profileFound := processingProfileForExtraction(message.GetExtractionVersion())
	if message == nil || !validEventIdentifier(message.GetBookId()) || len(message.GetSourceSha256()) != sha256.Size ||
		len(message.GetManifestSha256()) != sha256.Size || message.GetManifestByteSize() <= 0 || message.GetManifestByteSize() > int64(contracts.MaximumManifestBytes) ||
		message.GetPageCount() == 0 || message.GetPageCount() > maxProcessedPages ||
		message.GetChunkCount() == 0 || message.GetChunkCount() > maxProcessedChunks ||
		!validProcessingProfile(message) ||
		!profileFound || message.GetStructureVersion() != profile.structureVersion ||
		message.GetMaximumTokens() != profile.maximumTokens ||
		message.GetOverlapTokens() != profile.overlapTokens {
		return false
	}
	prefix := "books/" + message.GetBookId() + "/" + hex.EncodeToString(message.GetSourceSha256()) + "/"
	remainder, found := strings.CutPrefix(message.GetManifestReference(), prefix)
	if !found {
		return false
	}
	for _, configDigest := range profile.configDigests {
		if remainder == hex.EncodeToString(configDigest[:])+"/manifest.pb" {
			return true
		}
	}
	return false
}

func processingProfileForExtraction(extractionVersion string) (processingProfile, bool) {
	switch extractionVersion {
	case supportedM4Profile.extractionVersion:
		return supportedM4Profile, true
	case supportedM7EPUBProfile.extractionVersion:
		return supportedM7EPUBProfile, true
	case supportedM8PDFFilteredProfile.extractionVersion:
		return supportedM8PDFFilteredProfile, true
	case supportedM8EPUBFilteredProfile.extractionVersion:
		return supportedM8EPUBFilteredProfile, true
	default:
		return processingProfile{}, false
	}
}

func supportedIndexProfileDigest(value []byte) bool {
	return bytes.Equal(value, supportedM5ProfileDigest[:]) ||
		bytes.Equal(value, supportedM7EPUBIndexProfileDigest[:]) ||
		bytes.Equal(value, supportedM8PDFFilteredIndexProfileDigest[:]) ||
		bytes.Equal(value, supportedM8EPUBFilteredIndexProfileDigest[:])
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
