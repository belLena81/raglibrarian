package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

type processorRepository struct {
	accepted    bool
	acceptErr   error
	completeErr error
	retryErr    error
	failErr     error
	accepts     int
	completes   int
	retries     int
	fails       int
	failedJob   domain.ProcessingJob
}

func (r *processorRepository) Accept(_ context.Context, _ UploadedEvent, _ [32]byte, job domain.ProcessingJob, _ OutboxEvent) (domain.ProcessingJob, bool, error) {
	r.accepts++
	return job, r.accepted, r.acceptErr
}

func (r *processorRepository) Complete(_ context.Context, _ domain.ProcessingJob, _ ClaimToken, _ artifact.Result, _ OutboxEvent) error {
	r.completes++
	return r.completeErr
}

func (r *processorRepository) Retry(_ context.Context, _ domain.ProcessingJob, _ ClaimToken) error {
	r.retries++
	return r.retryErr
}

func (r *processorRepository) Fail(_ context.Context, job domain.ProcessingJob, _ ClaimToken, _ OutboxEvent) error {
	r.fails++
	r.failedJob = job
	return r.failErr
}

type processorSource struct {
	contents []byte
	size     int64
	err      error
}

func (s processorSource) Open(context.Context, string) (io.ReadCloser, int64, error) {
	if s.err != nil {
		return nil, 0, s.err
	}
	return io.NopCloser(bytes.NewReader(s.contents)), s.size, nil
}

type processorExtractor struct {
	err        error
	waitForCtx bool
	sourcePath string
}

type processorStreamingRunner struct{}

func (processorStreamingRunner) Run(context.Context, string, []string, int64) ([]byte, error) {
	return []byte("Pages: 1\nEncrypted: no\n"), nil
}

func (processorStreamingRunner) StreamPages(_ context.Context, _ string, _ []string, _ extractor.Limits, _ uint32, consume func(extractor.Page) error) error {
	return consume(extractor.Page{Number: 1, Text: "synthetic"})
}

func (e *processorExtractor) Extract(ctx context.Context, sourcePath string, consume func(extractor.Page) error) (extractor.DocumentInfo, error) {
	e.sourcePath = sourcePath
	if e.waitForCtx {
		<-ctx.Done()
		return extractor.DocumentInfo{}, ctx.Err()
	}
	if e.err != nil {
		return extractor.DocumentInfo{}, e.err
	}
	if err := consume(extractor.Page{Number: 1, Text: "synthetic text"}); err != nil {
		return extractor.DocumentInfo{}, err
	}
	return extractor.DocumentInfo{PageCount: 1}, nil
}

type processorChunker struct{}

func (processorChunker) AddPage(string, chunking.Page) ([]domain.Chunk, error) {
	return []domain.Chunk{{}}, nil
}

func (processorChunker) Finish(string) ([]domain.Chunk, error) { return nil, nil }

type processorWriter struct {
	result artifact.Result
	addErr error
	aborts int
	adds   int
}

func (w *processorWriter) Add(context.Context, domain.Chunk) error {
	w.adds++
	return w.addErr
}

func (w *processorWriter) Finalize(context.Context, uint32) (artifact.Result, error) {
	return w.result, nil
}

func (w *processorWriter) Abort(context.Context) error {
	w.aborts++
	return nil
}

type processorFactory struct {
	writer *processorWriter
}

func (processorFactory) NewChunker() (Chunker, error) { return processorChunker{}, nil }

func (f processorFactory) NewArtifactWriter(UploadedEvent, time.Time) (ArtifactWriter, error) {
	return f.writer, nil
}

type processorEvents struct {
	readyErr error
	started  int
	ready    int
	failed   int
}

func (e *processorEvents) Started(UploadedEvent, domain.ProcessingJob, time.Time) (OutboxEvent, error) {
	e.started++
	return OutboxEvent{ID: "started-1"}, nil
}

func (e *processorEvents) Ready(UploadedEvent, domain.ProcessingJob, artifact.Result, time.Time) (OutboxEvent, error) {
	e.ready++
	return OutboxEvent{ID: "ready-1"}, e.readyErr
}

func (e *processorEvents) Failed(UploadedEvent, domain.ProcessingJob, domain.FailureCategory, time.Time) (OutboxEvent, error) {
	e.failed++
	return OutboxEvent{ID: "failed-1"}, nil
}

func TestProcessorCompletesAndTreatsDuplicateAsDurableSuccess(t *testing.T) {
	processor, repository, writer, events, _ := newTestProcessor(t, processorOptions{})
	if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if repository.completes != 1 || repository.retries != 0 || repository.fails != 0 || writer.adds != 1 || writer.aborts != 0 || events.ready != 1 {
		t.Fatalf("unexpected success calls: repo=%#v writer=%#v events=%#v", repository, writer, events)
	}

	duplicate, duplicateRepository, duplicateWriter, _, _ := newTestProcessor(t, processorOptions{accepted: boolPointer(false)})
	if err := duplicate.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if duplicateRepository.accepts != 1 || duplicateRepository.completes != 0 || duplicateWriter.adds != 0 {
		t.Fatalf("duplicate was processed: repo=%#v writer=%#v", duplicateRepository, duplicateWriter)
	}
}

func TestProcessorFailsChecksumMismatchWithoutCreatingArtifacts(t *testing.T) {
	event := validProcessorEvent()
	event.SourceSHA256 = sha256.Sum256([]byte("different"))
	processor, repository, writer, _, _ := newTestProcessor(t, processorOptions{})
	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if repository.fails != 1 || repository.failedJob.Failure() != domain.FailureSourceIntegrityMismatch || writer.adds != 0 || writer.aborts != 0 {
		t.Fatalf("checksum result: repo=%#v writer=%#v", repository, writer)
	}
}

func TestProcessorPersistsRetryAndFinalTimeout(t *testing.T) {
	retrying, retryRepository, retryWriter, _, _ := newTestProcessor(t, processorOptions{sourceErr: errors.New("source unavailable"), maximumAttempts: 2})
	err := retrying.Process(context.Background(), validProcessorEvent())
	if !errors.Is(err, ErrProcessingDeferred) || retryRepository.retries != 1 || retryRepository.fails != 0 || retryWriter.aborts != 0 {
		t.Fatalf("retry result: err=%v repo=%#v writer=%#v", err, retryRepository, retryWriter)
	}

	final, finalRepository, finalWriter, _, _ := newTestProcessor(t, processorOptions{waitForContext: true, maximumAttempts: 1})
	if err = final.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if finalRepository.fails != 1 || finalRepository.failedJob.Failure() != domain.FailureProcessingTimeout || finalWriter.aborts != 1 {
		t.Fatalf("timeout result: repo=%#v writer=%#v", finalRepository, finalWriter)
	}
}

func TestProcessorRetriesInternalExtractorFailure(t *testing.T) {
	invalidExtractor := extractor.NewPoppler("pdfinfo", "pdftotext", extractor.Limits{}, nil)
	_, extractorErr := invalidExtractor.Extract(context.Background(), "source.pdf", func(extractor.Page) error { return nil })
	processor, repository, writer, _, _ := newTestProcessor(t, processorOptions{
		extractErr:      extractorErr,
		maximumAttempts: 2,
	})

	err := processor.Process(context.Background(), validProcessorEvent())
	if !errors.Is(err, ErrProcessingDeferred) || repository.retries != 1 || repository.fails != 0 || writer.aborts != 1 {
		t.Fatalf("internal extraction retry: err=%v repo=%#v writer=%#v", err, repository, writer)
	}
}

func TestProcessorDoesNotRetryStreamingLimitFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "chunk limit", err: chunking.ErrChunkLimit},
		{name: "artifact limit", err: artifact.ErrArtifactLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdfExtractor := extractor.NewPoppler(
				"pdfinfo",
				"pdftotext",
				extractor.Limits{MaximumPages: 2, MaximumPageBytes: 32, MaximumExtractedBytes: 64},
				processorStreamingRunner{},
			)
			_, extractErr := pdfExtractor.Extract(context.Background(), "source.pdf", func(extractor.Page) error {
				return test.err
			})
			processor, repository, writer, _, _ := newTestProcessor(t, processorOptions{
				extractErr:      extractErr,
				maximumAttempts: 2,
			})

			if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
				t.Fatal(err)
			}
			if repository.retries != 0 || repository.fails != 1 || repository.failedJob.Failure() != domain.FailureResourceLimitExceeded || writer.aborts != 1 {
				t.Fatalf("streaming limit result: repo=%#v writer=%#v", repository, writer)
			}
		})
	}
}

func TestProcessorAbortsArtifactsAndRemovesTemporaryInput(t *testing.T) {
	processor, repository, writer, _, pdfExtractor := newTestProcessor(t, processorOptions{extractErr: errors.New("extractor crashed"), maximumAttempts: 1})
	if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if repository.fails != 1 || writer.aborts != 1 {
		t.Fatalf("failure cleanup: repo=%#v writer=%#v", repository, writer)
	}
	if _, err := os.Stat(pdfExtractor.sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary source still exists: %q err=%v", pdfExtractor.sourcePath, err)
	}
}

func TestProcessorSurfacesRepositoryAndOutboxPersistenceFailures(t *testing.T) {
	completeFailure := errors.New("commit unavailable")
	processor, repository, _, _, _ := newTestProcessor(t, processorOptions{completeErr: completeFailure})
	err := processor.Process(context.Background(), validProcessorEvent())
	if !errors.Is(err, completeFailure) || FailureReason(err) != "complete_failed" || repository.completes != 1 {
		t.Fatalf("completion error = %v reason=%q", err, FailureReason(err))
	}

	processor, repository, writer, events, _ := newTestProcessor(t, processorOptions{readyErr: errors.New("encode ready event")})
	if err = processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if events.ready != 1 || events.failed != 1 || repository.fails != 1 || writer.aborts != 0 {
		t.Fatalf("ready event fallback: repo=%#v writer=%#v events=%#v", repository, writer, events)
	}
}

func TestClassifyRetriesInternalExtractorAndProcessingFailures(t *testing.T) {
	ctx := context.Background()
	invalidExtractor := extractor.NewPoppler("pdfinfo", "pdftotext", extractor.Limits{}, nil)
	_, extractorErr := invalidExtractor.Extract(ctx, "source.pdf", func(extractor.Page) error { return nil })

	tests := []struct {
		name      string
		err       error
		category  domain.FailureCategory
		permanent bool
	}{
		{
			name:      "internal extractor failure",
			err:       extractorErr,
			category:  domain.FailureInternalProcessing,
			permanent: false,
		},
		{
			name:      "internal processing failure",
			err:       processingError{category: domain.FailureInternalProcessing},
			category:  domain.FailureInternalProcessing,
			permanent: false,
		},
		{
			name:      "malformed document",
			err:       processingError{category: domain.FailureMalformedDocument},
			category:  domain.FailureMalformedDocument,
			permanent: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			category, permanent := classify(test.err, ctx)
			if category != test.category || permanent != test.permanent {
				t.Fatalf("classify() = (%q, %t), want (%q, %t)", category, permanent, test.category, test.permanent)
			}
		})
	}
}

type processorOptions struct {
	accepted        *bool
	sourceErr       error
	extractErr      error
	waitForContext  bool
	maximumAttempts int
	completeErr     error
	readyErr        error
}

func newTestProcessor(t *testing.T, options processorOptions) (*Processor, *processorRepository, *processorWriter, *processorEvents, *processorExtractor) {
	t.Helper()
	accepted := true
	if options.accepted != nil {
		accepted = *options.accepted
	}
	maximumAttempts := options.maximumAttempts
	if maximumAttempts == 0 {
		maximumAttempts = 4
	}
	contents := []byte("%PDF-1.7\nsynthetic")
	repository := &processorRepository{accepted: accepted, completeErr: options.completeErr}
	writer := &processorWriter{result: artifact.Result{ManifestReference: "books/book-1/source/profile/manifest.pb", ManifestSHA256: sha256.Sum256([]byte("manifest")), ManifestByteSize: 8, PageCount: 1, ChunkCount: 1}}
	events := &processorEvents{readyErr: options.readyErr}
	pdfExtractor := &processorExtractor{err: options.extractErr, waitForCtx: options.waitForContext}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	processor, err := NewProcessor(
		repository,
		processorSource{contents: contents, size: int64(len(contents)), err: options.sourceErr},
		pdfExtractor,
		processorFactory{writer: writer},
		events,
		func() (string, error) { return "job-1", nil },
		func() time.Time { return now },
		"worker-1",
		Config{
			MaximumSourceBytes:    25 << 20,
			MaximumTemporaryBytes: 25 << 20,
			TemporaryDirectory:    t.TempDir(),
			ProcessingTimeout:     10 * time.Millisecond,
			JobLease:              31 * time.Second,
			MaximumAttempts:       maximumAttempts,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return processor, repository, writer, events, pdfExtractor
}

func validProcessorEvent() UploadedEvent {
	contents := []byte("%PDF-1.7\nsynthetic")
	return UploadedEvent{
		EventID:         "event-1",
		BookID:          "book-1",
		ObjectReference: "originals/book-1.pdf",
		MediaType:       "application/pdf",
		CorrelationID:   "correlation-1",
		CausationID:     "causation-1",
		Producer:        "catalog-service",
		SchemaVersion:   "v1",
		IdempotencyKey:  "book-1",
		SourceSHA256:    sha256.Sum256(contents),
		ByteSize:        int64(len(contents)),
		OccurredAt:      time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC),
		Payload:         []byte("synthetic-protobuf-payload"),
	}
}

func boolPointer(value bool) *bool { return &value }
