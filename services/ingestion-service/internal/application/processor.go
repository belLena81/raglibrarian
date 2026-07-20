// Package application coordinates one durable PDF processing use case.
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

type DeferredError struct{ RetryAt time.Time }

func (e DeferredError) Error() string          { return ErrProcessingDeferred.Error() }
func (e DeferredError) Is(target error) bool   { return target == ErrProcessingDeferred }
func NewDeferredError(retryAt time.Time) error { return DeferredError{RetryAt: retryAt} }

type UploadedEvent struct {
	EventID, BookID, ObjectReference, MediaType, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	SourceSHA256                                                                                                     [32]byte
	ByteSize                                                                                                         int64
	OccurredAt                                                                                                       time.Time
	Payload                                                                                                          []byte
}

func (e UploadedEvent) Validate(maximumBytes int64) error {
	if !safeID(e.EventID) || !safeID(e.BookID) || !safeID(e.CorrelationID) || !safeID(e.CausationID) || e.IdempotencyKey != e.BookID || e.Producer != "catalog-service" || e.SchemaVersion != "v1" || e.MediaType != "application/pdf" || e.ByteSize < 1 || e.ByteSize > maximumBytes || e.OccurredAt.IsZero() || len(e.Payload) == 0 || len(e.Payload) > 256<<10 {
		return ErrInvalidEvent
	}
	if !validSourceReference(e.ObjectReference) {
		return ErrInvalidEvent
	}
	return nil
}

func validSourceReference(reference string) bool {
	if !strings.HasPrefix(reference, "originals/") || strings.Count(reference, "/") != 1 || !strings.HasSuffix(reference, ".pdf") || len(reference) > 512 {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(reference, "originals/"), ".pdf")
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
	Accept(context.Context, UploadedEvent, [32]byte, domain.ProcessingJob, OutboxEvent) (domain.ProcessingJob, bool, error)
	Complete(context.Context, domain.ProcessingJob, ClaimToken, artifact.Result, OutboxEvent) error
	Fail(context.Context, domain.ProcessingJob, ClaimToken, OutboxEvent) error
	Retry(context.Context, domain.ProcessingJob, ClaimToken) error
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
	Finish(string) ([]domain.Chunk, error)
}

type ArtifactWriter interface {
	Add(context.Context, domain.Chunk) error
	Finalize(context.Context, uint32) (artifact.Result, error)
	Abort(context.Context) error
}

type Factory interface {
	NewChunker() (Chunker, error)
	NewArtifactWriter(UploadedEvent, time.Time) (ArtifactWriter, error)
}

type EventFactory interface {
	Started(UploadedEvent, domain.ProcessingJob, time.Time) (OutboxEvent, error)
	Ready(UploadedEvent, domain.ProcessingJob, artifact.Result, time.Time) (OutboxEvent, error)
	Failed(UploadedEvent, domain.ProcessingJob, domain.FailureCategory, time.Time) (OutboxEvent, error)
}

type IDGenerator func() (string, error)
type Clock func() time.Time

type Config struct {
	MaximumSourceBytes    int64
	MaximumTemporaryBytes int64
	TemporaryDirectory    string
	ProcessingTimeout     time.Duration
	JobLease              time.Duration
	MaximumAttempts       int
	ConfigDigest          [32]byte
	Observer              PhaseObserver
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

type Processor struct {
	repository Repository
	sources    SourceReader
	extractor  Extractor
	factory    Factory
	events     EventFactory
	newID      IDGenerator
	now        Clock
	workerID   string
	config     Config
	observer   PhaseObserver
}

func NewProcessor(repository Repository, sources SourceReader, pdfExtractor Extractor, factory Factory, events EventFactory, newID IDGenerator, now Clock, workerID string, config Config) (*Processor, error) {
	if repository == nil || sources == nil || pdfExtractor == nil || factory == nil || events == nil || newID == nil || now == nil || !safeID(workerID) || config.MaximumSourceBytes < 1 || config.MaximumTemporaryBytes < config.MaximumSourceBytes || config.ProcessingTimeout <= 0 || config.JobLease < config.ProcessingTimeout+30*time.Second || config.MaximumAttempts < 1 || config.TemporaryDirectory == "" {
		return nil, errors.New("invalid processor configuration")
	}
	observer := config.Observer
	if observer == nil {
		observer = noopObserver{}
	}
	return &Processor{repository: repository, sources: sources, extractor: pdfExtractor, factory: factory, events: events, newID: newID, now: now, workerID: workerID, config: config, observer: observer}, nil
}

func (p *Processor) Process(parent context.Context, event UploadedEvent) error {
	totalStarted := p.now()
	defer func() { p.observer.ObservePhase(PhaseTotal, p.now().Sub(totalStarted)) }()
	if err := event.Validate(p.config.MaximumSourceBytes); err != nil {
		return err
	}
	jobID, err := p.newID()
	if err != nil {
		return errors.New("generate processing identity")
	}
	now := p.now().UTC()
	job, err := domain.NewProcessingJob(jobID, event.BookID, event.SourceSHA256, hex.EncodeToString(p.config.ConfigDigest[:]), now)
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
	var accepted bool
	job, accepted, err = p.repository.Accept(parent, event, payloadDigest, job, started)
	if err != nil {
		return operational("accept_failed", err)
	}
	if !accepted {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, p.config.ProcessingTimeout)
	defer cancel()
	result, processErr := p.processClaimed(ctx, event, job.CreatedAt())
	claim := ClaimToken{Owner: job.LeaseOwner(), Attempt: job.Attempts(), ExpiresAt: job.LeaseExpiresAt()}
	if processErr == nil {
		ready, readyErr := p.events.Ready(event, job, result, p.now().UTC())
		if readyErr != nil {
			return p.persistFailure(parent, event, &job, claim, domain.FailureInternalProcessing)
		}
		if err = job.Complete(p.workerID, result.ManifestReference, result.ManifestSHA256, result.ManifestByteSize, p.now().UTC()); err != nil {
			return err
		}
		return operational("complete_failed", p.repository.Complete(parent, job, claim, result, ready))
	}
	category, permanent := classify(processErr, ctx)
	if !permanent && job.Attempts() < p.config.MaximumAttempts {
		retryAt := p.now().UTC().Add(retryDelay(job.Attempts()))
		if err = job.ScheduleRetry(p.workerID, retryAt, p.now().UTC()); err != nil {
			return err
		}
		if err = p.repository.Retry(parent, job, claim); err != nil {
			return err
		}
		return NewDeferredError(retryAt)
	}
	return p.persistFailure(parent, event, &job, claim, category)
}

func (p *Processor) processClaimed(ctx context.Context, event UploadedEvent, generatedAt time.Time) (artifact.Result, error) {
	if err := ensureTemporaryCapacity(p.config.TemporaryDirectory, p.config.MaximumTemporaryBytes); err != nil {
		return artifact.Result{}, categorized(domain.FailureResourceLimitExceeded)
	}
	invocationDir, err := os.MkdirTemp(p.config.TemporaryDirectory, "ingestion-")
	if err != nil {
		return artifact.Result{}, errors.New("temporary storage unavailable")
	}
	_ = os.Chmod(invocationDir, 0o700) // #nosec G302 -- directories require execute permission and remain owner-only.
	defer func() { _ = os.RemoveAll(invocationDir) }()
	sourcePath := filepath.Join(invocationDir, "source.pdf")
	downloadStarted := p.now()
	err = p.download(ctx, event, sourcePath)
	p.observer.ObservePhase(PhaseDownload, p.now().Sub(downloadStarted))
	if err != nil {
		return artifact.Result{}, err
	}
	chunker, err := p.factory.NewChunker()
	if err != nil {
		return artifact.Result{}, err
	}
	// Job creation is the immutable processing acceptance time. Using it keeps
	// manifest bytes stable when an upload succeeded but the DB commit did not.
	writer, err := p.factory.NewArtifactWriter(event, generatedAt)
	if err != nil {
		return artifact.Result{}, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = writer.Abort(cleanupCtx)
		}
	}()
	var chunkCount uint32
	extractStarted := p.now()
	info, err := p.extractor.Extract(ctx, sourcePath, func(page extractor.Page) error {
		chunks, chunkErr := chunker.AddPage(event.BookID, chunking.Page{Number: page.Number, Text: page.Text})
		if chunkErr != nil {
			return chunkErr
		}
		for _, value := range chunks {
			if chunkErr = writer.Add(ctx, value); chunkErr != nil {
				return chunkErr
			}
			chunkCount++
		}
		return nil
	})
	if err != nil {
		p.observer.ObservePhase(PhaseExtractChunk, p.now().Sub(extractStarted))
		return artifact.Result{}, err
	}
	remaining, err := chunker.Finish(event.BookID)
	if err != nil {
		return artifact.Result{}, err
	}
	for _, value := range remaining {
		if err = writer.Add(ctx, value); err != nil {
			return artifact.Result{}, err
		}
		chunkCount++
	}
	p.observer.ObservePhase(PhaseExtractChunk, p.now().Sub(extractStarted))
	if chunkCount == 0 {
		return artifact.Result{}, categorized(domain.FailureNoExtractableText)
	}
	finalizeStarted := p.now()
	result, err := writer.Finalize(ctx, info.PageCount)
	p.observer.ObservePhase(PhaseArtifactFinalize, p.now().Sub(finalizeStarted))
	if err == nil {
		committed = true
	}
	return result, err
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
		return categorized(domain.FailureDependencyUnavailable)
	}
	defer func() { _ = reader.Close() }()
	if size != event.ByteSize || size > p.config.MaximumSourceBytes {
		return categorized(domain.FailureSourceIntegrityMismatch)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is inside a fresh private invocation directory.
	if err != nil {
		return categorized(domain.FailureDependencyUnavailable)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, event.ByteSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return categorized(domain.FailureDependencyUnavailable)
	}
	if written != event.ByteSize || written > p.config.MaximumSourceBytes || !equalBytes(hash.Sum(nil), event.SourceSHA256[:]) {
		return categorized(domain.FailureSourceIntegrityMismatch)
	}
	return nil
}

func (p *Processor) persistFailure(ctx context.Context, event UploadedEvent, job *domain.ProcessingJob, claim ClaimToken, category domain.FailureCategory) error {
	now := p.now().UTC()
	failed, err := p.events.Failed(event, *job, category, now)
	if err != nil {
		return err
	}
	if err = job.Fail(p.workerID, category, now); err != nil {
		return err
	}
	return operational("fail_persistence_failed", p.repository.Fail(ctx, *job, claim, failed))
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

func permanentProcessingFailure(category domain.FailureCategory) bool {
	switch category {
	case domain.FailureDependencyUnavailable, domain.FailureProcessingTimeout, domain.FailureInternalProcessing:
		return false
	default:
		return true
	}
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 2 * time.Minute
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
