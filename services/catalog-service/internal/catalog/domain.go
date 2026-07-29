// Package catalog contains Catalog's transport-independent domain model.
package catalog

import (
	"errors"
	"strings"
	"time"
)

const (
	maxTitleLength  = 256
	maxAuthorLength = 256
	maxTags         = 20
	maxTagLength    = 64
)

var (
	ErrInvalidMetadata           = errors.New("invalid book metadata")
	ErrUnauthorizedActor         = errors.New("unauthorized actor")
	ErrInvalidTransition         = errors.New("invalid book status transition")
	ErrInvalidCommand            = errors.New("invalid lifecycle command")
	ErrConflictingProcessingFact = errors.New("conflicting processing fact")
)

// BookStatus records the Catalog-owned publication lifecycle.
type BookStatus string

const (
	BookStatusPending    BookStatus = "pending"
	BookStatusProcessing BookStatus = "processing"
	BookStatusIndexed    BookStatus = "indexed"
	BookStatusFailed     BookStatus = "failed"
	BookStatusReindexing BookStatus = "reindexing"
	BookStatusDeleting   BookStatus = "deleting"
	BookStatusDeleted    BookStatus = "deleted"
)

// BookProcessingStage is Catalog's user-visible projection of asynchronous work.
type BookProcessingStage string

const (
	BookStageQueued      BookProcessingStage = "queued"
	BookStageExtracting  BookProcessingStage = "extracting"
	BookStageChunksReady BookProcessingStage = "chunks_ready"
	BookStageIndexed     BookProcessingStage = "indexed"
	BookStageFailed      BookProcessingStage = "failed"
)

// ProcessingFailureCategory is a closed, sanitized processing outcome.
type ProcessingFailureCategory string

const (
	FailureEncryptedDocument       ProcessingFailureCategory = "encrypted_document"
	FailureExtractionNotPermitted  ProcessingFailureCategory = "extraction_not_permitted"
	FailureMalformedDocument       ProcessingFailureCategory = "malformed_document"
	FailureUnsupportedDocument     ProcessingFailureCategory = "unsupported_document"
	FailureNoExtractableText       ProcessingFailureCategory = "no_extractable_text"
	FailureResourceLimitExceeded   ProcessingFailureCategory = "resource_limit_exceeded"
	FailureSourceIntegrityMismatch ProcessingFailureCategory = "source_integrity_mismatch"
	FailureProcessingTimeout       ProcessingFailureCategory = "processing_timeout"
	FailureDependencyUnavailable   ProcessingFailureCategory = "dependency_unavailable"
	FailureInternalProcessingError ProcessingFailureCategory = "internal_processing_error"
	FailureManifestIntegrity       ProcessingFailureCategory = "manifest_integrity"
	FailureIncompatibleProfile     ProcessingFailureCategory = "incompatible_profile"
	FailureEmbeddingUnavailable    ProcessingFailureCategory = "embedding_unavailable"
	FailureVectorStoreUnavailable  ProcessingFailureCategory = "vector_store_unavailable"
	FailureIndexingTimeout         ProcessingFailureCategory = "indexing_timeout"
	FailureInternalIndexingError   ProcessingFailureCategory = "internal_indexing_error"
)

// ProcessingFactKind identifies facts reported by Ingestion.
type ProcessingFactKind uint8

const (
	ProcessingStarted ProcessingFactKind = iota + 1
	ProcessingChunksReady
	ProcessingFailed
	ProcessingIndexed
	ProcessingIndexingFailed
)

// ProcessingFact contains only state needed by the Book aggregate.
type ProcessingFact struct {
	Kind            ProcessingFactKind
	FailureCategory ProcessingFailureCategory
	FailureDetail   string
	OccurredAt      time.Time
}

// Actor is a live principal forwarded only by the authenticated Edge service.
type Actor struct {
	UserID      string
	Role        string
	Status      string
	MaskedEmail string
}

func (a Actor) CanRead() bool {
	if a.Status != "active" || a.UserID == "" {
		return false
	}
	return a.Role == "reader" || a.Role == "librarian" || a.Role == "admin"
}

func (a Actor) CanUpload() bool {
	return a.Status == "active" && a.UserID != "" && (a.Role == "librarian" || a.Role == "admin")
}

func (a Actor) CanManage() bool {
	return a.CanUpload()
}

// BookMetadata is immutable upload metadata.
type BookMetadata struct {
	Title  string
	Author string
	Year   int
	Tags   []string
}

// Book is Catalog's aggregate. Storage details never leave this package.
type Book struct {
	ID                        string
	Metadata                  BookMetadata
	ProcessingStatus          BookStatus
	CreatedAt                 time.Time
	ObjectReference           string
	Checksum                  [32]byte
	ByteSize                  int64
	MediaType                 string
	Preview                   string
	ActorID                   string
	ProcessingStage           BookProcessingStage
	ProcessingFailureCategory ProcessingFailureCategory
	ProcessingFailureDetail   string
	ProcessingUpdatedAt       time.Time
	ProcessingVersion         int64
	LifecycleVersion          int64
	ManifestReference         string
	ManifestChecksum          [32]byte
	DeleteCommandID           string
	ArtifactsDeleted          bool
	IndexDeleted              bool
	OriginalDeleted           bool
}

func (b Book) CanReindex() bool {
	if b.ManifestReference == "" || b.ManifestChecksum == [32]byte{} {
		return false
	}
	if b.ProcessingStatus == BookStatusIndexed {
		return true
	}
	return b.ProcessingStatus == BookStatusFailed && reindexableFailureCategory(b.ProcessingFailureCategory)
}

// ApplyProcessingFact applies an idempotent, monotonic Ingestion fact.
func (b *Book) ApplyProcessingFact(fact ProcessingFact) (bool, error) {
	if fact.OccurredAt.IsZero() {
		return false, ErrConflictingProcessingFact
	}
	switch fact.Kind {
	case ProcessingStarted:
		if b.ProcessingStage == BookStageExtracting {
			return false, nil
		}
		if b.ProcessingStage == BookStageChunksReady || b.ProcessingStage == BookStageIndexed || b.ProcessingStage == BookStageFailed {
			return false, nil
		}
		if b.ProcessingStatus != BookStatusPending || b.ProcessingStage != BookStageQueued {
			return false, ErrConflictingProcessingFact
		}
		if err := b.TransitionTo(BookStatusProcessing); err != nil {
			return false, err
		}
		b.ProcessingStage = BookStageExtracting
	case ProcessingChunksReady:
		if b.ProcessingStage == BookStageChunksReady || b.ProcessingStage == BookStageIndexed ||
			(b.ProcessingStage == BookStageFailed && validIndexingFailureCategory(b.ProcessingFailureCategory)) {
			return false, nil
		}
		if b.ProcessingStage == BookStageFailed {
			return false, ErrConflictingProcessingFact
		}
		if b.ProcessingStatus == BookStatusPending {
			if err := b.TransitionTo(BookStatusProcessing); err != nil {
				return false, err
			}
		} else if b.ProcessingStatus != BookStatusProcessing {
			return false, ErrConflictingProcessingFact
		}
		b.ProcessingStage = BookStageChunksReady
		b.ProcessingFailureCategory = ""
		b.ProcessingFailureDetail = ""
	case ProcessingFailed:
		if !validFailureCategory(fact.FailureCategory) || !validFailureDetail(fact.FailureDetail) {
			return false, ErrConflictingProcessingFact
		}
		if b.ProcessingStage == BookStageIndexed ||
			(b.ProcessingStage == BookStageFailed && validIndexingFailureCategory(b.ProcessingFailureCategory)) {
			return false, nil
		}
		if b.ProcessingStage == BookStageFailed && b.ProcessingFailureCategory == fact.FailureCategory &&
			b.ProcessingFailureDetail == fact.FailureDetail {
			return false, nil
		}
		if b.ProcessingStage == BookStageChunksReady || b.ProcessingStatus == BookStatusFailed {
			return false, ErrConflictingProcessingFact
		}
		if b.ProcessingStatus == BookStatusPending {
			if err := b.TransitionTo(BookStatusProcessing); err != nil {
				return false, err
			}
		}
		if err := b.TransitionTo(BookStatusFailed); err != nil {
			return false, err
		}
		b.ProcessingStage = BookStageFailed
		b.ProcessingFailureCategory = fact.FailureCategory
		b.ProcessingFailureDetail = fact.FailureDetail
	case ProcessingIndexed:
		if b.ProcessingStage == BookStageIndexed && b.ProcessingStatus == BookStatusIndexed {
			return false, nil
		}
		if b.ProcessingStatus == BookStatusReindexing && b.ProcessingStage == BookStageChunksReady {
			if err := b.TransitionTo(BookStatusIndexed); err != nil {
				return false, err
			}
			b.ProcessingStage = BookStageIndexed
			b.ProcessingFailureCategory = ""
			b.ProcessingFailureDetail = ""
			break
		}
		if b.ProcessingStatus == BookStatusPending && b.ProcessingStage == BookStageQueued {
			if err := b.TransitionTo(BookStatusProcessing); err != nil {
				return false, err
			}
		}
		if (b.ProcessingStage != BookStageQueued && b.ProcessingStage != BookStageExtracting && b.ProcessingStage != BookStageChunksReady) || b.ProcessingStatus != BookStatusProcessing {
			return false, ErrConflictingProcessingFact
		}
		if err := b.TransitionTo(BookStatusIndexed); err != nil {
			return false, err
		}
		b.ProcessingStage = BookStageIndexed
		b.ProcessingFailureCategory = ""
		b.ProcessingFailureDetail = ""
	case ProcessingIndexingFailed:
		if !validIndexingFailureCategory(fact.FailureCategory) || !validFailureDetail(fact.FailureDetail) {
			return false, ErrConflictingProcessingFact
		}
		if b.ProcessingStage == BookStageFailed && b.ProcessingStatus == BookStatusFailed &&
			b.ProcessingFailureCategory == fact.FailureCategory && b.ProcessingFailureDetail == fact.FailureDetail {
			return false, nil
		}
		if b.ProcessingStatus == BookStatusReindexing && b.ProcessingStage == BookStageChunksReady {
			if err := b.TransitionTo(BookStatusFailed); err != nil {
				return false, err
			}
			b.ProcessingStage = BookStageFailed
			b.ProcessingFailureCategory = fact.FailureCategory
			b.ProcessingFailureDetail = fact.FailureDetail
			break
		}
		if b.ProcessingStatus == BookStatusPending && b.ProcessingStage == BookStageQueued {
			if err := b.TransitionTo(BookStatusProcessing); err != nil {
				return false, err
			}
		}
		if (b.ProcessingStage != BookStageQueued && b.ProcessingStage != BookStageExtracting && b.ProcessingStage != BookStageChunksReady) || b.ProcessingStatus != BookStatusProcessing {
			return false, ErrConflictingProcessingFact
		}
		if err := b.TransitionTo(BookStatusFailed); err != nil {
			return false, err
		}
		b.ProcessingStage = BookStageFailed
		b.ProcessingFailureCategory = fact.FailureCategory
		b.ProcessingFailureDetail = fact.FailureDetail
	default:
		return false, ErrConflictingProcessingFact
	}
	b.ProcessingVersion++
	b.ProcessingUpdatedAt = fact.OccurredAt.UTC()
	return true, nil
}

func validIndexingFailureCategory(category ProcessingFailureCategory) bool {
	switch category {
	case FailureManifestIntegrity, FailureIncompatibleProfile, FailureEmbeddingUnavailable,
		FailureVectorStoreUnavailable, FailureResourceLimitExceeded, FailureIndexingTimeout,
		FailureInternalIndexingError:
		return true
	default:
		return false
	}
}

func reindexableFailureCategory(category ProcessingFailureCategory) bool {
	switch category {
	case FailureDependencyUnavailable, FailureProcessingTimeout, FailureResourceLimitExceeded,
		FailureInternalProcessingError, FailureEmbeddingUnavailable, FailureVectorStoreUnavailable,
		FailureIndexingTimeout, FailureInternalIndexingError:
		return true
	default:
		return false
	}
}

func validFailureCategory(category ProcessingFailureCategory) bool {
	switch category {
	case FailureEncryptedDocument, FailureExtractionNotPermitted, FailureMalformedDocument,
		FailureUnsupportedDocument, FailureNoExtractableText, FailureResourceLimitExceeded,
		FailureSourceIntegrityMismatch, FailureProcessingTimeout, FailureDependencyUnavailable,
		FailureInternalProcessingError:
		return true
	default:
		return false
	}
}

func validFailureDetail(detail string) bool {
	if detail == "" {
		return true
	}
	if len(detail) > 128 {
		return false
	}
	for _, character := range detail {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// TransitionTo validates and applies a Catalog-owned lifecycle transition.
// Upload is the only Milestone 3 writer and creates pending books; later
// consumers must use the same transition table when reporting processing facts.
func (b *Book) TransitionTo(next BookStatus) error {
	if !validTransition(b.ProcessingStatus, next) {
		return ErrInvalidTransition
	}
	b.ProcessingStatus = next
	return nil
}

func validTransition(current, next BookStatus) bool {
	if current == next {
		return false
	}
	if next == BookStatusDeleting {
		switch current {
		case BookStatusPending, BookStatusProcessing, BookStatusIndexed, BookStatusFailed, BookStatusReindexing:
			return true
		default:
			return false
		}
	}
	switch current {
	case BookStatusPending:
		return next == BookStatusProcessing
	case BookStatusProcessing:
		return next == BookStatusIndexed || next == BookStatusFailed
	case BookStatusIndexed:
		return next == BookStatusReindexing
	case BookStatusReindexing:
		return next == BookStatusIndexed || next == BookStatusFailed
	case BookStatusDeleting:
		return next == BookStatusDeleted
	default:
		return false
	}
}

func ValidateMetadata(metadata BookMetadata) error {
	return validateMetadataAt(metadata, time.Now().UTC())
}

func validateMetadataAt(metadata BookMetadata, now time.Time) error {
	if len(strings.TrimSpace(metadata.Title)) == 0 || len(metadata.Title) > maxTitleLength ||
		len(strings.TrimSpace(metadata.Author)) == 0 || len(metadata.Author) > maxAuthorLength ||
		metadata.Year < 0 || metadata.Year > now.UTC().Year()+1 || len(metadata.Tags) > maxTags {
		return ErrInvalidMetadata
	}
	for _, tag := range metadata.Tags {
		if len(strings.TrimSpace(tag)) == 0 || len(tag) > maxTagLength {
			return ErrInvalidMetadata
		}
	}
	return nil
}
