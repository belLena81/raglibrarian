// Package application coordinates durable document processing use cases.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

var (
	ErrInvalidEvent                 = errors.New("invalid upload event")
	ErrConflictingEvent             = errors.New("conflicting upload event")
	ErrProcessingDeferred           = errors.New("processing deferred")
	ErrUnsupportedProcessingProfile = errors.New("unsupported processing profile")
)

type operationalError struct {
	code  string
	cause error
}

func (e operationalError) Error() string { return e.code }
func (e operationalError) Unwrap() error { return e.cause }

func operational(code string, cause error) error {
	if cause == nil {
		return nil
	}
	return operationalError{code: code, cause: cause}
}

// FailureReason returns an allowlisted content-free diagnostic reason.
func FailureReason(err error) string {
	var target operationalError
	if errors.As(err, &target) {
		return target.code
	}
	return "processing_error"
}

// FailureDetail returns an allowlisted stage-level diagnostic detail.
func FailureDetail(err error) string {
	if err == nil {
		return ""
	}
	var target operationalError
	if errors.As(err, &target) {
		return FailureDetail(target.cause)
	}
	var deferred DeferredError
	if errors.As(err, &deferred) {
		return FailureDetail(deferred.Cause)
	}
	var staged stagedError
	if errors.As(err, &staged) {
		return stagedFailureDetail(staged)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "processing_timeout"
	}
	return ""
}

func stagedFailureDetail(err stagedError) string {
	if err.detail != "chunk_sequence_invalid" || err.cause == nil {
		return err.detail
	}
	cause := strings.TrimSpace(err.cause.Error())
	if cause == "" {
		return err.detail
	}
	cause = strings.Join(strings.Fields(cause), "_")
	const maximumDetailLength = 512
	if len(cause) > maximumDetailLength {
		cause = cause[:maximumDetailLength]
	}
	return err.detail + "_" + cause
}

type DeferredError struct {
	RetryAt time.Time
	Cause   error
}

func (e DeferredError) Error() string        { return ErrProcessingDeferred.Error() }
func (e DeferredError) Is(target error) bool { return target == ErrProcessingDeferred }
func (e DeferredError) Unwrap() error        { return e.Cause }

func NewDeferredError(retryAt time.Time, cause ...error) error {
	var wrapped error
	if len(cause) > 0 {
		wrapped = cause[0]
	}
	return DeferredError{RetryAt: retryAt, Cause: wrapped}
}

type UploadedEvent struct {
	EventID, BookID, ObjectReference, MediaType, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	SourceSHA256                                                                                                     [32]byte
	ByteSize                                                                                                         int64
	LifecycleVersion                                                                                                 int64
	OccurredAt                                                                                                       time.Time
	Payload                                                                                                          []byte
	ExtractionVersion                                                                                                string
}

func (e UploadedEvent) Validate(maximumBytes int64) error {
	if !safeID(e.EventID) || !safeID(e.BookID) || !safeID(e.CorrelationID) || !safeID(e.CausationID) || e.IdempotencyKey != e.BookID || e.Producer != "catalog-service" || e.SchemaVersion != "v1" || e.LifecycleVersion < 1 || e.ByteSize < 1 || e.ByteSize > maximumBytes || e.OccurredAt.IsZero() || len(e.Payload) == 0 || len(e.Payload) > contracts.MaximumBrokerMessageBytes {
		return ErrInvalidEvent
	}
	if !validSourceReference(e.ObjectReference, e.MediaType) {
		return ErrInvalidEvent
	}
	return nil
}

func validSourceReference(reference, mediaType string) bool {
	extension := ""
	switch mediaType {
	case MediaTypePDF:
		extension = ".pdf"
	case MediaTypeEPUB:
		extension = ".epub"
	default:
		return false
	}
	if !strings.HasPrefix(reference, "originals/") || strings.Count(reference, "/") != 1 || !strings.HasSuffix(reference, extension) || len(reference) > 512 {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(reference, "originals/"), extension)
	if name == "" {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

type OutboxEvent struct {
	ID, Type   string
	Payload    []byte
	OccurredAt time.Time
}

type Repository interface {
	Accept(context.Context, UploadedEvent, [32]byte, domain.ProcessingJob, OutboxEvent) (AcceptResult, error)
	AwaitSelection(context.Context, domain.ProcessingJob, ClaimToken, OutboxEvent) error
	AcceptContentSelection(context.Context, ContentSelectionRecord, string, time.Time, time.Duration) (domain.ProcessingJob, bool, error)
	LoadContentSelection(context.Context, string) (ContentSelectionRecord, error)
	LoadUploadedPayload(context.Context, string) ([]byte, error)
	AcceptDeletion(context.Context, DeletionEvent, [32]byte, OutboxEvent, time.Time) error
	Complete(context.Context, domain.ProcessingJob, ClaimToken, artifact.Result, OutboxEvent) error
	Fail(context.Context, domain.ProcessingJob, ClaimToken, OutboxEvent) error
	Retry(context.Context, domain.ProcessingJob, ClaimToken) error
}

type AcceptResult struct {
	Job                      domain.ProcessingJob
	Accepted                 bool
	ContentSelectionTimedOut bool
}

type ClaimToken struct {
	Owner     string
	Attempt   int
	ExpiresAt time.Time
}

type SourceReader interface {
	Open(context.Context, string) (io.ReadCloser, int64, error)
}

type Extractor interface {
	Extract(context.Context, string, func(extractor.Page) error) (extractor.DocumentInfo, error)
}

type Chunker interface {
	AddPage(string, chunking.Page) ([]domain.Chunk, error)
	Boundary(string) ([]domain.Chunk, error)
	Finish(string) ([]domain.Chunk, error)
}

type ArtifactWriter interface {
	Add(context.Context, domain.Chunk) error
	Finalize(context.Context, uint32, *artifact.ContentSelection) (artifact.Result, error)
	Abort(context.Context) error
}

type Factory interface {
	NewChunker() (Chunker, error)
	NewArtifactWriter(UploadedEvent, time.Time) (ArtifactWriter, error)
	ConfigDigest(string) ([32]byte, error)
	ExtractionVersion(string) (string, error)
	ContentSelectionProfile() ContentSelectionProfile
}

type EventFactory interface {
	Started(UploadedEvent, domain.ProcessingJob, time.Time) (OutboxEvent, error)
	ContentSelectionRequested(UploadedEvent, domain.ProcessingJob, time.Time) (OutboxEvent, error)
	Ready(UploadedEvent, domain.ProcessingJob, artifact.Result, time.Time) (OutboxEvent, error)
	Failed(UploadedEvent, domain.ProcessingJob, domain.FailureCategory, string, time.Time) (OutboxEvent, error)
	ArtifactsDeleted(DeletionEvent, time.Time) (OutboxEvent, error)
}

type IDGenerator func() (string, error)
type Clock func() time.Time

type Config struct {
	MaximumSourceBytes          int64
	MaximumTemporaryBytes       int64
	TemporaryDirectory          string
	ProcessingTimeout           time.Duration
	PersistenceTimeout          time.Duration
	ArtifactAbortTimeout        time.Duration
	JobLease                    time.Duration
	ContentSelectionWaitTimeout time.Duration
	MaximumAttempts             int
	FirstRetryDelay             time.Duration
	SecondRetryDelay            time.Duration
	SubsequentRetryDelay        time.Duration
	Observer                    PhaseObserver
	Diagnostics                 ProcessorDiagnostics
	DecodeUploaded              UploadDecoder
	DecodeContentSelection      ContentSelectionDecoder
}

type ProcessingPhase uint8

const (
	PhaseDownload ProcessingPhase = iota
	PhaseExtractChunk
	PhaseArtifactFinalize
	PhaseTotal
)

type PhaseObserver interface {
	ObservePhase(ProcessingPhase, time.Duration)
}

type noopObserver struct{}

func (noopObserver) ObservePhase(ProcessingPhase, time.Duration) {}

type ProcessorDiagnostics interface {
	ReadyEventPrepared(eventID, bookID, readyEventID string)
	CompletionPersisted(eventID, bookID, readyEventID string)
	CompletionPersistenceFailed(eventID, bookID, readyEventID, reason string)
	FailurePersisted(eventID, bookID, failedEventID, category, detail string)
}

type noopDiagnostics struct{}

func (noopDiagnostics) ReadyEventPrepared(string, string, string)                  {}
func (noopDiagnostics) CompletionPersisted(string, string, string)                 {}
func (noopDiagnostics) CompletionPersistenceFailed(string, string, string, string) {}
func (noopDiagnostics) FailurePersisted(string, string, string, string, string)    {}

type Processor struct {
	repository      Repository
	sources         SourceReader
	extractors      ExtractorSelector
	factory         Factory
	events          EventFactory
	newID           IDGenerator
	now             Clock
	workerID        string
	config          Config
	observer        PhaseObserver
	diagnostics     ProcessorDiagnostics
	selection       ContentSelectionProfile
	decodeUpload    UploadDecoder
	decodeSelection ContentSelectionDecoder
}

type stagedError struct {
	detail string
	cause  error
}

func (e stagedError) Error() string { return e.detail }
func (e stagedError) Unwrap() error { return e.cause }

func stage(detail string, cause error) error {
	if cause == nil {
		return nil
	}
	return stagedError{detail: detail, cause: cause}
}

func NewProcessor(repository Repository, sources SourceReader, extractors ExtractorSelector, factory Factory, events EventFactory, newID IDGenerator, now Clock, workerID string, config Config) (*Processor, error) {
	if factory == nil {
		return nil, errors.New("invalid processor configuration")
	}
	selectionProfile := factory.ContentSelectionProfile()
	if repository == nil || sources == nil || extractors == nil || events == nil || newID == nil || now == nil || !safeID(workerID) || config.MaximumSourceBytes < 1 || config.MaximumTemporaryBytes < config.MaximumSourceBytes || config.ProcessingTimeout <= 0 || config.PersistenceTimeout <= 0 || config.ArtifactAbortTimeout <= 0 || config.JobLease < config.ProcessingTimeout+30*time.Second || config.ContentSelectionWaitTimeout <= 0 || config.MaximumAttempts < 1 || config.FirstRetryDelay <= 0 || config.SecondRetryDelay <= 0 || config.SubsequentRetryDelay <= 0 || config.TemporaryDirectory == "" || selectionProfile.Validate() != nil || (selectionProfile.Mode != ContentSelectionDisabled && (config.DecodeUploaded == nil || config.DecodeContentSelection == nil)) {
		return nil, errors.New("invalid processor configuration")
	}
	observer := config.Observer
	if observer == nil {
		observer = noopObserver{}
	}
	diagnostics := config.Diagnostics
	if diagnostics == nil {
		diagnostics = noopDiagnostics{}
	}
	return &Processor{repository: repository, sources: sources, extractors: extractors, factory: factory, events: events, newID: newID, now: now, workerID: workerID, config: config, observer: observer, diagnostics: diagnostics, selection: selectionProfile, decodeUpload: config.DecodeUploaded, decodeSelection: config.DecodeContentSelection}, nil
}

func (p *Processor) Process(parent context.Context, event UploadedEvent) error {
	totalStarted := p.now()
	defer func() { p.observer.ObservePhase(PhaseTotal, p.now().Sub(totalStarted)) }()
	if err := event.Validate(p.config.MaximumSourceBytes); err != nil {
		return err
	}
	adapter, err := p.extractors.Select(event.MediaType)
	if err != nil {
		return err
	}
	event.ExtractionVersion, err = p.factory.ExtractionVersion(event.MediaType)
	if err != nil {
		return err
	}
	configDigest, err := p.factory.ConfigDigest(event.MediaType)
	if err != nil {
		return err
	}
	jobID, err := p.newID()
	if err != nil {
		return errors.New("generate processing identity")
	}
	now := p.now().UTC()
	job, err := domain.NewProcessingJob(jobID, event.BookID, event.SourceSHA256, hex.EncodeToString(configDigest[:]), now)
	if err != nil {
		return err
	}
	if err = job.Claim(p.workerID, now, p.config.JobLease); err != nil {
		return err
	}
	started, err := p.events.Started(event, job, now)
	if err != nil {
		return err
	}
	payloadDigest := sha256.Sum256(event.Payload)
	acceptCtx, acceptCancel := context.WithTimeout(parent, p.config.PersistenceTimeout)
	accepted, err := p.repository.Accept(acceptCtx, event, payloadDigest, job, started)
	acceptCancel()
	if err != nil {
		return operational("accept_failed", err)
	}
	if !accepted.Accepted {
		return nil
	}
	job = accepted.Job
	if p.selection.Mode != ContentSelectionDisabled {
		if accepted.ContentSelectionTimedOut {
			timeoutSelection, timeoutErr := p.timeoutSelection(event, job)
			if timeoutErr != nil {
				return timeoutErr
			}
			return p.processAccepted(parent, event, adapter, job, timeoutSelection)
		}
		loadCtx, loadCancel := context.WithTimeout(parent, p.config.PersistenceTimeout)
		selectionResult, loadErr := decodeStoredSelection(loadCtx, p.repository, p.decodeSelection, job.ID(), p.selection.MaximumRanges)
		loadCancel()
		switch {
		case loadErr == nil:
			if err = p.validateSelectionForJob(event, job, selectionResult); err != nil {
				return err
			}
			return p.processAccepted(parent, event, adapter, job, &selectionResult)
		case !errors.Is(loadErr, ErrContentSelectionNotFound):
			return operational("selection_load_failed", loadErr)
		}
		return p.awaitSelection(parent, event, &job)
	}
	return p.processAccepted(parent, event, adapter, job, nil)
}

func (p *Processor) timeoutSelection(event UploadedEvent, job domain.ProcessingJob) (*ContentSelectionResult, error) {
	configDigestBytes, err := hex.DecodeString(job.ConfigDigest())
	if err != nil || len(configDigestBytes) != sha256.Size {
		return nil, ErrUnsupportedProcessingProfile
	}
	var configDigest [sha256.Size]byte
	copy(configDigest[:], configDigestBytes)
	fallback := "processing_timeout"
	if p.selection.Mode == ContentSelectionObservation {
		fallback = "observation"
	}
	return &ContentSelectionResult{
		JobID:                   job.ID(),
		BookID:                  event.BookID,
		SourceSHA256:            event.SourceSHA256,
		ProcessingProfileDigest: configDigest,
		PolicyDigest:            p.selection.PolicyDigest(),
		MediaType:               event.MediaType,
		Mode:                    string(p.selection.Mode),
		PolicyVersion:           p.selection.PolicyVersion,
		ParserVersion:           p.selection.ParserVersion,
		ModelSHA256:             p.selection.ModelSHA256,
		FallbackReason:          fallback,
		FallbackUnfiltered:      true,
		LifecycleVersion:        event.LifecycleVersion,
	}, nil
}

func (p *Processor) awaitSelection(parent context.Context, event UploadedEvent, job *domain.ProcessingJob) error {
	now := p.now().UTC()
	request, err := p.events.ContentSelectionRequested(event, *job, now)
	if err != nil {
		return operational("selection_request_failed", err)
	}
	claim := ClaimToken{Owner: job.LeaseOwner(), Attempt: job.Attempts(), ExpiresAt: job.LeaseExpiresAt()}
	if err = job.AwaitContentSelection(p.workerID, now); err != nil {
		return err
	}
	persistCtx, persistCancel := p.persistenceContext(parent)
	defer persistCancel()
	if err = p.repository.AwaitSelection(persistCtx, *job, claim, request); err != nil {
		return operational("selection_await_failed", err)
	}
	return nil
}

// ProcessContentSelection durably accepts a selection result and resumes its
// waiting job using the original upload envelope stored by Ingestion.
func (p *Processor) ProcessContentSelection(parent context.Context, selectionResult ContentSelectionResult) error {
	if err := selectionResult.Validate(p.selection); err != nil {
		return err
	}
	loadCtx, loadCancel := context.WithTimeout(parent, p.config.PersistenceTimeout)
	payload, err := p.repository.LoadUploadedPayload(loadCtx, selectionResult.JobID)
	loadCancel()
	if err != nil {
		return operational("upload_load_failed", err)
	}
	event, err := p.decodeUpload(payload)
	if err != nil || event.Validate(p.config.MaximumSourceBytes) != nil {
		return ErrInvalidEvent
	}
	if err = validateSelectionForEvent(event, selectionResult); err != nil {
		return err
	}
	now := p.now().UTC()
	acceptCtx, acceptCancel := context.WithTimeout(parent, p.config.PersistenceTimeout)
	job, accepted, err := p.repository.AcceptContentSelection(acceptCtx, selectionResult.record(now), p.workerID, now, p.config.JobLease)
	acceptCancel()
	if err != nil {
		return operational("selection_accept_failed", err)
	}
	if !accepted {
		return nil
	}
	adapter, err := p.extractors.Select(event.MediaType)
	if err != nil {
		return err
	}
	event.ExtractionVersion, err = p.factory.ExtractionVersion(event.MediaType)
	if err != nil {
		return err
	}
	if err = p.validateSelectionForJob(event, job, selectionResult); err != nil {
		return err
	}
	return p.processAccepted(parent, event, adapter, job, &selectionResult)
}

func (p *Processor) processAccepted(parent context.Context, event UploadedEvent, adapter ExtractionAdapter, job domain.ProcessingJob, selectionResult *ContentSelectionResult) error {
	var err error
	ctx, cancel := context.WithTimeout(parent, p.config.ProcessingTimeout)
	defer cancel()
	result, processErr := p.processClaimed(ctx, event, adapter, job.CreatedAt(), selectionResult)
	claim := ClaimToken{Owner: job.LeaseOwner(), Attempt: job.Attempts(), ExpiresAt: job.LeaseExpiresAt()}
	if processErr == nil {
		ready, readyErr := p.events.Ready(event, job, result, p.now().UTC())
		if readyErr != nil {
			persistCtx, persistCancel := p.persistenceContext(parent)
			defer persistCancel()
			return p.persistFailure(persistCtx, event, &job, claim, domain.FailureInternalProcessing, "ready_event_prepare_failed")
		}
		p.diagnostics.ReadyEventPrepared(event.EventID, event.BookID, ready.ID)
		if err = job.Complete(p.workerID, result.ManifestReference, result.ManifestSHA256, result.ManifestByteSize, p.now().UTC()); err != nil {
			return err
		}
		persistCtx, persistCancel := p.persistenceContext(parent)
		defer persistCancel()
		if err = p.repository.Complete(persistCtx, job, claim, result, ready); err != nil {
			err = operational("complete_failed", err)
			p.diagnostics.CompletionPersistenceFailed(event.EventID, event.BookID, ready.ID, FailureReason(err))
			return err
		}
		p.diagnostics.CompletionPersisted(event.EventID, event.BookID, ready.ID)
		return nil
	}
	category, permanent := classify(processErr, ctx)
	if !permanent && job.Attempts() < p.config.MaximumAttempts {
		retryAt := p.now().UTC().Add(p.retryDelay(job.Attempts()))
		if err = job.ScheduleRetry(p.workerID, retryAt, p.now().UTC()); err != nil {
			return err
		}
		persistCtx, persistCancel := p.persistenceContext(parent)
		defer persistCancel()
		if err = p.repository.Retry(persistCtx, job, claim); err != nil {
			return err
		}
		return NewDeferredError(retryAt, processErr)
	}
	persistCtx, persistCancel := p.persistenceContext(parent)
	defer persistCancel()
	return p.persistFailure(persistCtx, event, &job, claim, category, FailureDetail(processErr))
}

func (p *Processor) persistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent.Err() != nil {
		return context.WithTimeout(context.Background(), p.config.PersistenceTimeout)
	}
	return context.WithTimeout(parent, p.config.PersistenceTimeout)
}

func (p *Processor) processClaimed(ctx context.Context, event UploadedEvent, adapter ExtractionAdapter, generatedAt time.Time, selectionResult *ContentSelectionResult) (artifact.Result, error) {
	if err := ensureTemporaryCapacity(p.config.TemporaryDirectory, p.config.MaximumTemporaryBytes); err != nil {
		return artifact.Result{}, stage("temporary_capacity_unavailable", categorized(domain.FailureResourceLimitExceeded))
	}
	invocationDir, err := os.MkdirTemp(p.config.TemporaryDirectory, "ingestion-")
	if err != nil {
		return artifact.Result{}, stage("temporary_directory_unavailable", err)
	}
	_ = os.Chmod(invocationDir, 0o700) // #nosec G302 -- directories require execute permission and remain owner-only.
	defer func() { _ = os.RemoveAll(invocationDir) }()
	sourcePath := filepath.Join(invocationDir, "source"+adapter.Extension)
	downloadStarted := p.now()
	err = p.download(ctx, event, sourcePath)
	p.observer.ObservePhase(PhaseDownload, p.now().Sub(downloadStarted))
	if err != nil {
		return artifact.Result{}, err
	}
	effectiveSelection := selectionResult
	if selectionResult != nil && p.selection.Mode == ContentSelectionEnforcement && selectionResult.FallbackReason == "none" {
		preflight, preflightErr := adapter.Extractor.Extract(ctx, sourcePath, func(extractor.Page) error { return nil })
		if preflightErr != nil {
			return artifact.Result{}, stage(extractorFailureDetail(preflightErr), preflightErr)
		}
		if preflight.PageCount != selectionResult.OriginalLocationCount {
			fallback := *selectionResult
			fallback.FallbackReason = "ambiguous_mapping"
			fallback.FallbackUnfiltered = true
			fallback.OriginalLocationCount = preflight.PageCount
			fallback.Ranges = nil
			effectiveSelection = &fallback
		}
	}
	chunker, err := p.factory.NewChunker()
	if err != nil {
		return artifact.Result{}, stage("chunker_unavailable", err)
	}
	// Job creation is the immutable processing acceptance time. Using it keeps
	// manifest bytes stable when an upload succeeded but the DB commit did not.
	writer, err := p.factory.NewArtifactWriter(event, generatedAt)
	if err != nil {
		return artifact.Result{}, stage("artifact_writer_unavailable", err)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), p.config.ArtifactAbortTimeout)
			defer cancel()
			_ = writer.Abort(cleanupCtx)
		}
	}()
	var chunkCount uint32
	var previousChunk domain.Chunk
	var havePreviousChunk bool
	validateAndAddChunk := func(value domain.Chunk) error {
		if havePreviousChunk {
			overlapTooLarge := false
			if value.TokenStart() < previousChunk.TokenEnd() {
				overlapTooLarge = previousChunk.TokenEnd()-value.TokenStart() > uint64(chunking.DefaultOverlapTokens)
			}
			if value.Order() != previousChunk.Order()+1 ||
				value.TokenStart() < previousChunk.TokenStart() ||
				value.TokenEnd() <= previousChunk.TokenEnd() ||
				overlapTooLarge {
				return stage("chunk_sequence_invalid", fmt.Errorf("invalid chunk sequence previous_order=%d order=%d previous_token_start=%d previous_token_end=%d token_start=%d token_end=%d overlap_tokens=%d",
					previousChunk.Order(), value.Order(), previousChunk.TokenStart(), previousChunk.TokenEnd(), value.TokenStart(), value.TokenEnd(), chunking.DefaultOverlapTokens))
			}
		}
		if err = writer.Add(ctx, value); err != nil {
			return stage("artifact_add_failed", err)
		}
		chunkCount++
		previousChunk = value
		havePreviousChunk = true
		return nil
	}
	extractStarted := p.now()
	var rangeIndex int
	inExcludedRange := false
	info, err := adapter.Extractor.Extract(ctx, sourcePath, func(page extractor.Page) error {
		if effectiveSelection != nil && p.selection.Mode == ContentSelectionEnforcement && effectiveSelection.FallbackReason == "none" {
			for rangeIndex < len(effectiveSelection.Ranges) && page.Number > effectiveSelection.Ranges[rangeIndex].End {
				rangeIndex++
				inExcludedRange = false
			}
			if rangeIndex < len(effectiveSelection.Ranges) && page.Number >= effectiveSelection.Ranges[rangeIndex].Start && page.Number <= effectiveSelection.Ranges[rangeIndex].End {
				if !inExcludedRange {
					chunks, boundaryErr := chunker.Boundary(event.BookID)
					if boundaryErr != nil {
						return stage("chunk_boundary_failed", boundaryErr)
					}
					for _, value := range chunks {
						if boundaryErr = validateAndAddChunk(value); boundaryErr != nil {
							return boundaryErr
						}
					}
					inExcludedRange = true
				}
				return nil
			}
		}
		chunks, chunkErr := chunker.AddPage(event.BookID, chunking.Page{Number: page.Number, Text: page.Text})
		if chunkErr != nil {
			return stage("chunk_page_failed", chunkErr)
		}
		for _, value := range chunks {
			if chunkErr = validateAndAddChunk(value); chunkErr != nil {
				return chunkErr
			}
		}
		return nil
	})
	if err != nil {
		p.observer.ObservePhase(PhaseExtractChunk, p.now().Sub(extractStarted))
		return artifact.Result{}, stage(extractorFailureDetail(err), err)
	}
	if effectiveSelection != nil && effectiveSelection.OriginalLocationCount != 0 && info.PageCount != effectiveSelection.OriginalLocationCount {
		return artifact.Result{}, stage("content_selection_ordinal_mismatch", categorized(domain.FailureInternalProcessing))
	}
	remaining, err := chunker.Finish(event.BookID)
	if err != nil {
		return artifact.Result{}, stage("chunk_finalize_failed", err)
	}
	for _, value := range remaining {
		if err = validateAndAddChunk(value); err != nil {
			return artifact.Result{}, err
		}
	}
	p.observer.ObservePhase(PhaseExtractChunk, p.now().Sub(extractStarted))
	if chunkCount == 0 {
		return artifact.Result{}, stage("no_extractable_text", categorized(domain.FailureNoExtractableText))
	}
	finalizeStarted := p.now()
	result, err := writer.Finalize(ctx, info.PageCount, p.selectionAudit(effectiveSelection, info.PageCount))
	p.observer.ObservePhase(PhaseArtifactFinalize, p.now().Sub(finalizeStarted))
	if err == nil {
		committed = true
	}
	return result, stage("artifact_finalize_failed", err)
}

func ensureTemporaryCapacity(directory string, required int64) error {
	var status syscall.Statfs_t
	if err := syscall.Statfs(directory, &status); err != nil {
		return err
	}
	available := int64(status.Bavail) * int64(status.Bsize) // #nosec G115 -- filesystem counters are bounded by int64 on supported targets.
	if available < required {
		return errors.New("insufficient temporary storage")
	}
	return nil
}

func (p *Processor) download(ctx context.Context, event UploadedEvent, path string) error {
	reader, size, err := p.sources.Open(ctx, event.ObjectReference)
	if err != nil {
		return stage("source_open_failed", categorized(domain.FailureDependencyUnavailable))
	}
	defer func() { _ = reader.Close() }()
	if size != event.ByteSize || size > p.config.MaximumSourceBytes {
		return stage("source_size_mismatch", categorized(domain.FailureSourceIntegrityMismatch))
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is inside a fresh private invocation directory.
	if err != nil {
		return stage("source_file_create_failed", categorized(domain.FailureDependencyUnavailable))
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, event.ByteSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return stage("source_copy_failed", categorized(domain.FailureDependencyUnavailable))
	}
	if written != event.ByteSize || written > p.config.MaximumSourceBytes || !equalBytes(hash.Sum(nil), event.SourceSHA256[:]) {
		return stage("source_checksum_mismatch", categorized(domain.FailureSourceIntegrityMismatch))
	}
	return nil
}

func (p *Processor) persistFailure(ctx context.Context, event UploadedEvent, job *domain.ProcessingJob, claim ClaimToken, category domain.FailureCategory, detail string) error {
	now := p.now().UTC()
	failed, err := p.events.Failed(event, *job, category, detail, now)
	if err != nil {
		return err
	}
	if err = job.Fail(p.workerID, category, now); err != nil {
		return err
	}
	if err = p.repository.Fail(ctx, *job, claim, failed); err != nil {
		return operational("fail_persistence_failed", err)
	}
	p.diagnostics.FailurePersisted(event.EventID, event.BookID, failed.ID, string(category), detail)
	return nil
}

type processingError struct{ category domain.FailureCategory }

func (e processingError) Error() string                 { return string(e.category) }
func categorized(category domain.FailureCategory) error { return processingError{category: category} }

func classify(err error, ctx context.Context) (domain.FailureCategory, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.FailureProcessingTimeout, false
	}
	if errors.Is(err, chunking.ErrChunkLimit) || errors.Is(err, artifact.ErrArtifactLimit) {
		return domain.FailureResourceLimitExceeded, true
	}
	if category, ok := extractor.FailureCategory(err); ok {
		return category, permanentProcessingFailure(category)
	}
	var typed processingError
	if errors.As(err, &typed) {
		return typed.category, permanentProcessingFailure(typed.category)
	}
	return domain.FailureInternalProcessing, false
}

func extractorFailureDetail(err error) string {
	if cause, ok := extractor.ConsumerCause(err); ok {
		return FailureDetail(cause)
	}
	if detail, ok := extractor.FailureDetail(err); ok {
		return detail
	}
	return "extract_failed"
}

func permanentProcessingFailure(category domain.FailureCategory) bool {
	switch category {
	case domain.FailureDependencyUnavailable, domain.FailureProcessingTimeout, domain.FailureInternalProcessing:
		return false
	default:
		return true
	}
}

func (p *Processor) retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return p.config.FirstRetryDelay
	case 2:
		return p.config.SecondRetryDelay
	default:
		return p.config.SubsequentRetryDelay
	}
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func (e UploadedEvent) String() string {
	return fmt.Sprintf("UploadedEvent{event:%q,book:%q,size:%d}", e.EventID, e.BookID, e.ByteSize)
}
