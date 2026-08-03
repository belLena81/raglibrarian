package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/indexprofile"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

type processorRepository struct {
	accepted          bool
	selectionTimedOut bool
	acceptErr         error
	acceptWait        bool
	completeErr       error
	retryErr          error
	failErr           error
	deletionWait      bool
	accepts           int
	deletionCalls     int
	completes         int
	retries           int
	fails             int
	failedJob         domain.ProcessingJob
	acceptCtxErr      error
	deletionCtxErr    error
	retryCtxErr       error
	failCtxErr        error
	awaits            int
	selectionAccepted bool
	selectionRecord   ContentSelectionRecord
	selectionJob      domain.ProcessingJob
	uploadedPayload   []byte
}

func (r *processorRepository) Accept(ctx context.Context, _ UploadedEvent, _ [32]byte, job domain.ProcessingJob, _ OutboxEvent) (AcceptResult, error) {
	r.accepts++
	if r.acceptWait {
		<-ctx.Done()
		r.acceptCtxErr = ctx.Err()
		return AcceptResult{Job: job}, ctx.Err()
	}
	return AcceptResult{Job: job, Accepted: r.accepted, ContentSelectionTimedOut: r.selectionTimedOut}, r.acceptErr
}

func (r *processorRepository) AcceptDeletion(ctx context.Context, _ DeletionEvent, _ [32]byte, _ OutboxEvent, _ time.Time) error {
	r.deletionCalls++
	if r.deletionWait {
		<-ctx.Done()
		r.deletionCtxErr = ctx.Err()
		return ctx.Err()
	}
	return nil
}

func (r *processorRepository) AwaitSelection(_ context.Context, job domain.ProcessingJob, _ ClaimToken, _ OutboxEvent) error {
	r.awaits++
	r.selectionJob = job
	return nil
}

func (r *processorRepository) AcceptContentSelection(_ context.Context, record ContentSelectionRecord, _ string, _ time.Time, _ time.Duration) (domain.ProcessingJob, bool, error) {
	r.selectionRecord = record
	return r.selectionJob, r.selectionAccepted, nil
}

func (r *processorRepository) LoadContentSelection(context.Context, string) (ContentSelectionRecord, error) {
	if len(r.selectionRecord.Payload) == 0 {
		return ContentSelectionRecord{}, ErrContentSelectionNotFound
	}
	return r.selectionRecord, nil
}

func (r *processorRepository) LoadUploadedPayload(context.Context, string) ([]byte, error) {
	return append([]byte(nil), r.uploadedPayload...), nil
}

func (r *processorRepository) Complete(_ context.Context, _ domain.ProcessingJob, _ ClaimToken, _ artifact.Result, _ OutboxEvent) error {
	r.completes++
	return r.completeErr
}

func (r *processorRepository) Retry(ctx context.Context, _ domain.ProcessingJob, _ ClaimToken) error {
	r.retries++
	r.retryCtxErr = ctx.Err()
	return r.retryErr
}

func (r *processorRepository) Fail(ctx context.Context, job domain.ProcessingJob, _ ClaimToken, _ OutboxEvent) error {
	r.fails++
	r.failedJob = job
	r.failCtxErr = ctx.Err()
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
	pages      []extractor.Page
}

type processorStreamingRunner struct{}

func (processorStreamingRunner) Run(context.Context, string, []string, int64) ([]byte, error) {
	return []byte("Pages: 1\nEncrypted: no\n"), nil
}

func (processorStreamingRunner) StreamPages(ctx context.Context, _ string, _ []string, limits extractor.Limits, expectedPages uint32, consume func(extractor.Page) error) error {
	return (extractor.ExecRunner{}).StreamPages(ctx, "sh", []string{"-c", "printf 'synthetic\\f'"}, limits, expectedPages, consume)
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
	pages := e.pages
	if len(pages) == 0 {
		pages = []extractor.Page{{Number: 1, Text: "synthetic text"}}
	}
	for _, page := range pages {
		if err := consume(page); err != nil {
			return extractor.DocumentInfo{}, err
		}
	}
	return extractor.DocumentInfo{PageCount: uint32(len(pages))}, nil // #nosec G115 -- test pages are bounded.
}

type processorChunker struct{}

func (processorChunker) AddPage(string, chunking.Page) ([]domain.Chunk, error) {
	return []domain.Chunk{{}}, nil
}

func (processorChunker) Finish(string) ([]domain.Chunk, error)   { return nil, nil }
func (processorChunker) Boundary(string) ([]domain.Chunk, error) { return nil, nil }

type processorSequenceChunker struct {
	add    []domain.Chunk
	finish []domain.Chunk
}

func (c processorSequenceChunker) AddPage(string, chunking.Page) ([]domain.Chunk, error) {
	return c.add, nil
}

func (c processorSequenceChunker) Finish(string) ([]domain.Chunk, error) {
	return c.finish, nil
}
func (c processorSequenceChunker) Boundary(string) ([]domain.Chunk, error) { return nil, nil }

type selectionTrackingChunker struct {
	pages      []uint32
	boundaries int
	nextOrder  uint64
}

func (c *selectionTrackingChunker) AddPage(bookID string, page chunking.Page) ([]domain.Chunk, error) {
	c.pages = append(c.pages, page.Number)
	value, err := domain.NewChunk(domain.ChunkInput{
		ID: fmt.Sprintf("chunk-%d", c.nextOrder), BookID: bookID, Order: c.nextOrder,
		Text: page.Text, PageStart: page.Number, PageEnd: page.Number, TokenStart: c.nextOrder, TokenEnd: c.nextOrder + 1,
	})
	if err != nil {
		return nil, err
	}
	c.nextOrder++
	return []domain.Chunk{value}, nil
}

func (c *selectionTrackingChunker) Boundary(string) ([]domain.Chunk, error) {
	c.boundaries++
	return nil, nil
}

func (c *selectionTrackingChunker) Finish(string) ([]domain.Chunk, error) { return nil, nil }

type processorWriter struct {
	result      artifact.Result
	addErr      error
	finalizeErr error
	selection   *artifact.ContentSelection
	event       UploadedEvent
	aborts      int
	adds        int
}

func (w *processorWriter) Add(context.Context, domain.Chunk) error {
	w.adds++
	return w.addErr
}

func (w *processorWriter) Finalize(_ context.Context, _ uint32, selection *artifact.ContentSelection) (artifact.Result, error) {
	w.selection = selection
	return w.result, w.finalizeErr
}

func (w *processorWriter) Abort(context.Context) error {
	w.aborts++
	return nil
}

type processorFactory struct {
	writer    *processorWriter
	chunker   Chunker
	selection ContentSelectionProfile
}

func (f processorFactory) NewChunker() (Chunker, error) {
	if f.chunker != nil {
		return f.chunker, nil
	}
	return processorChunker{}, nil
}

func (f processorFactory) NewArtifactWriter(event UploadedEvent, _ time.Time) (ArtifactWriter, error) {
	f.writer.event = event
	return f.writer, nil
}

func (processorFactory) ConfigDigest(mediaType string) ([32]byte, error) {
	if mediaType != MediaTypePDF && mediaType != MediaTypeEPUB {
		return [32]byte{}, ErrUnsupportedProcessingProfile
	}
	return sha256.Sum256([]byte(mediaType)), nil
}

func (f processorFactory) ExtractionVersion(mediaType string) (string, error) {
	if mediaType != MediaTypePDF && mediaType != MediaTypeEPUB {
		return "", ErrUnsupportedProcessingProfile
	}
	if f.ContentSelectionProfile().Mode != ContentSelectionDisabled {
		return indexprofile.ExtractionPDFFiltered, nil
	}
	return extractor.ExtractionVersion, nil
}

func (f processorFactory) ContentSelectionProfile() ContentSelectionProfile {
	if f.selection.Mode == "" {
		return ContentSelectionProfile{Mode: ContentSelectionDisabled}
	}
	return f.selection
}

type processorEvents struct {
	readyErr           error
	started            int
	selectionRequested int
	ready              int
	failed             int
	startedEvent       UploadedEvent
	readyEvent         UploadedEvent
}

type processorDiagnostics struct {
	readyPrepared     int
	completionPersist int
	completionFailed  int
	lastReason        string
	failurePersisted  int
	lastDetail        string
}

func (d *processorDiagnostics) ReadyEventPrepared(_, _, _ string)  { d.readyPrepared++ }
func (d *processorDiagnostics) CompletionPersisted(_, _, _ string) { d.completionPersist++ }
func (d *processorDiagnostics) CompletionPersistenceFailed(_, _, _, reason string) {
	d.completionFailed++
	d.lastReason = reason
}
func (d *processorDiagnostics) FailurePersisted(_, _, _, _, detail string) {
	d.failurePersisted++
	d.lastDetail = detail
}

func (e *processorEvents) Started(event UploadedEvent, _ domain.ProcessingJob, _ time.Time) (OutboxEvent, error) {
	e.started++
	e.startedEvent = event
	return OutboxEvent{ID: "started-1"}, nil
}

func (e *processorEvents) ContentSelectionRequested(UploadedEvent, domain.ProcessingJob, time.Time) (OutboxEvent, error) {
	e.selectionRequested++
	return OutboxEvent{ID: "selection-request-1", Type: "ingestion.book.content-selection-requested.v1", Payload: []byte{1}, OccurredAt: time.Now().UTC()}, nil
}

func (e *processorEvents) Ready(event UploadedEvent, _ domain.ProcessingJob, _ artifact.Result, _ time.Time) (OutboxEvent, error) {
	e.ready++
	e.readyEvent = event
	return OutboxEvent{ID: "ready-1"}, e.readyErr
}

func (e *processorEvents) Failed(UploadedEvent, domain.ProcessingJob, domain.FailureCategory, string, time.Time) (OutboxEvent, error) {
	e.failed++
	return OutboxEvent{ID: "failed-1"}, nil
}

func (e *processorEvents) ArtifactsDeleted(DeletionEvent, time.Time) (OutboxEvent, error) {
	return OutboxEvent{ID: "artifacts-deleted", Type: "ingestion.book.artifacts-deleted.v1", Payload: []byte{1}}, nil
}

func TestProcessorCompletesAndTreatsDuplicateAsDurableSuccess(t *testing.T) {
	processor, repository, writer, events, diagnostics, _ := newTestProcessor(t, processorOptions{})
	if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if repository.completes != 1 || repository.retries != 0 || repository.fails != 0 || writer.adds != 1 || writer.aborts != 0 || events.ready != 1 {
		t.Fatalf("unexpected success calls: repo=%#v writer=%#v events=%#v", repository, writer, events)
	}
	if diagnostics.readyPrepared != 1 || diagnostics.completionPersist != 1 || diagnostics.completionFailed != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}

	duplicate, duplicateRepository, duplicateWriter, _, _, _ := newTestProcessor(t, processorOptions{accepted: boolPointer(false)})
	if err := duplicate.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if duplicateRepository.accepts != 1 || duplicateRepository.completes != 0 || duplicateWriter.adds != 0 {
		t.Fatalf("duplicate was processed: repo=%#v writer=%#v", duplicateRepository, duplicateWriter)
	}
}

func TestProcessorPersistsAwaitingSelectionBeforeExtraction(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	processor, repository, writer, events, _, extractor := newTestProcessor(t, processorOptions{
		selection:       profile,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return validProcessorEvent(), nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return ContentSelectionResult{}, nil },
	})
	if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if repository.awaits != 1 || repository.selectionJob.State() != domain.JobAwaitingSelection || events.selectionRequested != 1 {
		t.Fatalf("selection wait = repo=%#v events=%#v", repository, events)
	}
	if writer.adds != 0 || extractor.sourcePath != "" || repository.completes != 0 {
		t.Fatalf("processing started before selection: writer=%#v source=%q", writer, extractor.sourcePath)
	}
}

func TestProcessorFailsOpenWhenContentSelectionTimesOut(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	event := validProcessorEvent()
	job := processingSelectionJob(t, event)
	chunker := &selectionTrackingChunker{}
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		selection:         profile,
		selectionTimedOut: true,
		selectionJob:      job,
		decodeUploaded:    func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection:   func([]byte, int) (ContentSelectionResult, error) { return ContentSelectionResult{}, nil },
		chunker:           chunker,
		pages:             []extractor.Page{{Number: 1, Text: "first"}, {Number: 2, Text: "second"}},
	})

	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	if repository.awaits != 0 || repository.completes != 1 || fmt.Sprint(chunker.pages) != "[1 2]" {
		t.Fatalf("timeout recovery = awaits %d completes %d pages %v", repository.awaits, repository.completes, chunker.pages)
	}
	if writer.selection == nil || writer.selection.FallbackReason != "processing_timeout" || len(writer.selection.Ranges) != 0 {
		t.Fatalf("timeout selection audit = %#v", writer.selection)
	}
}

func TestProcessorObservationTimeoutUsesObservationAudit(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionObservation)
	event := validProcessorEvent()
	job := processingSelectionJob(t, event)
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		selection:         profile,
		selectionTimedOut: true,
		selectionJob:      job,
		decodeUploaded:    func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection:   func([]byte, int) (ContentSelectionResult, error) { return ContentSelectionResult{}, nil },
		pages:             []extractor.Page{{Number: 1, Text: "first"}},
	})

	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	if repository.awaits != 0 || repository.completes != 1 {
		t.Fatalf("observation timeout recovery = repo %#v", repository)
	}
	if writer.selection == nil || writer.selection.FallbackReason != "observation" || len(writer.selection.Ranges) != 0 {
		t.Fatalf("observation timeout audit = %#v", writer.selection)
	}
}

func TestProcessorUsesLegacyExtractionVersionWhenSelectionIsDisabled(t *testing.T) {
	processor, _, writer, events, _, _ := newTestProcessor(t, processorOptions{})

	if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if events.startedEvent.ExtractionVersion != extractor.ExtractionVersion {
		t.Fatalf("started extraction version = %q, want %q", events.startedEvent.ExtractionVersion, extractor.ExtractionVersion)
	}
	if writer.event.ExtractionVersion != extractor.ExtractionVersion {
		t.Fatalf("manifest extraction version = %q, want %q", writer.event.ExtractionVersion, extractor.ExtractionVersion)
	}
	if events.readyEvent.ExtractionVersion != extractor.ExtractionVersion {
		t.Fatalf("ready extraction version = %q, want %q", events.readyEvent.ExtractionVersion, extractor.ExtractionVersion)
	}
}

func TestProcessorUsesFilteredExtractionVersionWhenSelectionIsEnabled(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	event := validProcessorEvent()
	result := validSelectionResult(event, profile)
	job := processingSelectionJob(t, event)
	initialProcessor, _, _, initialEvents, _, _ := newTestProcessor(t, processorOptions{
		selection:       profile,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return result, nil },
	})

	if err := initialProcessor.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if initialEvents.startedEvent.ExtractionVersion != indexprofile.ExtractionPDFFiltered {
		t.Fatalf("started extraction version = %q", initialEvents.startedEvent.ExtractionVersion)
	}

	resumeProcessor, _, writer, resumeEvents, _, _ := newTestProcessor(t, processorOptions{
		selection:         profile,
		selectionAccepted: true,
		selectionJob:      job,
		uploadedPayload:   event.Payload,
		decodeUploaded:    func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection:   func([]byte, int) (ContentSelectionResult, error) { return result, nil },
	})
	if err := resumeProcessor.ProcessContentSelection(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if writer.event.ExtractionVersion != indexprofile.ExtractionPDFFiltered {
		t.Fatalf("manifest extraction version = %q", writer.event.ExtractionVersion)
	}
	if resumeEvents.readyEvent.ExtractionVersion != indexprofile.ExtractionPDFFiltered {
		t.Fatalf("ready extraction version = %q", resumeEvents.readyEvent.ExtractionVersion)
	}
}

func TestProcessorEnforcesSelectionAndRecordsManifestAudit(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	event := validProcessorEvent()
	result := validSelectionResult(event, profile)
	result.OriginalLocationCount = 4
	result.Ranges = []ContentSelectionRange{{Start: 1, End: 1, Reason: "title"}}
	chunker := &selectionTrackingChunker{}
	job := processingSelectionJob(t, event)
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		selection: profile, selectionAccepted: true, selectionJob: job, uploadedPayload: event.Payload,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return result, nil },
		chunker:         chunker,
		pages:           []extractor.Page{{Number: 1, Text: "drop"}, {Number: 2, Text: "keep two"}, {Number: 3, Text: "keep three"}, {Number: 4, Text: "keep four"}},
	})
	if err := processor.ProcessContentSelection(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(chunker.pages) != "[2 3 4]" || chunker.boundaries != 1 || writer.adds != 3 || repository.completes != 1 {
		t.Fatalf("enforcement = pages=%v boundaries=%d writer=%#v repo=%#v", chunker.pages, chunker.boundaries, writer, repository)
	}
	if writer.selection == nil || writer.selection.Mode != "enforcement" || len(writer.selection.Ranges) != 1 || writer.selection.OriginalLocationCount != 4 {
		t.Fatalf("manifest selection = %#v", writer.selection)
	}
}

func TestProcessorObservationRetainsEveryLocation(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionObservation)
	event := validProcessorEvent()
	result := validSelectionResult(event, profile)
	result.OriginalLocationCount = 2
	result.FallbackReason = "observation"
	result.FallbackUnfiltered = true
	chunker := &selectionTrackingChunker{}
	job := processingSelectionJob(t, event)
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		selection: profile, selectionAccepted: true, selectionJob: job, uploadedPayload: event.Payload,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return result, nil },
		chunker:         chunker,
		pages:           []extractor.Page{{Number: 1, Text: "first"}, {Number: 2, Text: "second"}},
	})
	if err := processor.ProcessContentSelection(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(chunker.pages) != "[1 2]" || chunker.boundaries != 0 || repository.completes != 1 {
		t.Fatalf("observation = pages=%v boundaries=%d repo=%#v", chunker.pages, chunker.boundaries, repository)
	}
	if writer.selection == nil || writer.selection.FallbackReason != "observation" || len(writer.selection.Ranges) != 0 {
		t.Fatalf("observation audit = %#v", writer.selection)
	}
}

func TestProcessorFailOpenWithoutParserOrdinalCountUsesExtractorCount(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	event := validProcessorEvent()
	result := validSelectionResult(event, profile)
	result.OriginalLocationCount = 0
	result.FallbackReason = "invalid_output"
	result.FallbackUnfiltered = true
	chunker := &selectionTrackingChunker{}
	job := processingSelectionJob(t, event)
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		selection: profile, selectionAccepted: true, selectionJob: job, uploadedPayload: event.Payload,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return result, nil },
		chunker:         chunker,
		pages:           []extractor.Page{{Number: 1, Text: "first"}, {Number: 2, Text: "second"}},
	})
	if err := processor.ProcessContentSelection(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(chunker.pages) != "[1 2]" || chunker.boundaries != 0 || repository.completes != 1 {
		t.Fatalf("fail open = pages=%v boundaries=%d repo=%#v", chunker.pages, chunker.boundaries, repository)
	}
	if writer.selection == nil || writer.selection.OriginalLocationCount != 2 || writer.selection.FallbackReason != "invalid_output" {
		t.Fatalf("fail-open audit = %#v", writer.selection)
	}

	invalid := result
	invalid.FallbackReason = "none"
	invalid.FallbackUnfiltered = false
	if err := invalid.Validate(profile); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("zero-count filtered result error = %v", err)
	}
}

func TestProcessorOrdinalMismatchReplaysUnfilteredWithAudit(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	event := validProcessorEvent()
	result := validSelectionResult(event, profile)
	result.OriginalLocationCount = 4
	result.Ranges = []ContentSelectionRange{{Start: 1, End: 1, Reason: "title"}}
	chunker := &selectionTrackingChunker{}
	job := processingSelectionJob(t, event)
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		selection: profile, selectionAccepted: true, selectionJob: job, uploadedPayload: event.Payload,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return result, nil },
		chunker:         chunker,
		pages:           []extractor.Page{{Number: 1, Text: "first"}, {Number: 2, Text: "second"}},
	})
	if err := processor.ProcessContentSelection(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(chunker.pages) != "[1 2]" || chunker.boundaries != 0 || repository.completes != 1 {
		t.Fatalf("mismatch fallback = pages=%v boundaries=%d repo=%#v", chunker.pages, chunker.boundaries, repository)
	}
	if writer.selection == nil || writer.selection.OriginalLocationCount != 2 || writer.selection.FallbackReason != "ambiguous_mapping" || len(writer.selection.Ranges) != 0 {
		t.Fatalf("mismatch audit = %#v", writer.selection)
	}
}

func TestProcessorRejectsOverPolicySelectionBeforePersistence(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	event := validProcessorEvent()
	result := validSelectionResult(event, profile)
	result.OriginalLocationCount = 2
	result.Ranges = []ContentSelectionRange{{Start: 1, End: 1, Reason: "title"}}
	processor, repository, _, _, _, _ := newTestProcessor(t, processorOptions{
		selection:       profile,
		decodeUploaded:  func([]byte) (UploadedEvent, error) { return event, nil },
		decodeSelection: func([]byte, int) (ContentSelectionResult, error) { return result, nil },
	})
	if err := processor.ProcessContentSelection(context.Background(), result); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
	if len(repository.selectionRecord.Payload) != 0 {
		t.Fatal("invalid selection reached persistence")
	}
}

func TestProcessorBoundsInitialAcceptanceWithPersistenceTimeout(t *testing.T) {
	processor, repository, writer, events, _, _ := newTestProcessor(t, processorOptions{
		acceptWait:         true,
		persistenceTimeout: 5 * time.Millisecond,
	})

	err := processor.Process(context.Background(), validProcessorEvent())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Process() error = %v, want persistence deadline", err)
	}
	if !errors.Is(repository.acceptCtxErr, context.DeadlineExceeded) {
		t.Fatalf("Accept() context error = %v, want deadline exceeded", repository.acceptCtxErr)
	}
	if repository.accepts != 1 || writer.adds != 0 || events.ready != 0 {
		t.Fatalf("unexpected downstream processing: repository=%#v writer=%#v events=%#v", repository, writer, events)
	}
}

func TestProcessorFailsChecksumMismatchWithoutCreatingArtifacts(t *testing.T) {
	event := validProcessorEvent()
	event.SourceSHA256 = sha256.Sum256([]byte("different"))
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{})
	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if repository.fails != 1 || repository.failedJob.Failure() != domain.FailureSourceIntegrityMismatch || writer.adds != 0 || writer.aborts != 0 {
		t.Fatalf("checksum result: repo=%#v writer=%#v", repository, writer)
	}
}

func TestFailureDetailReturnsStageDetail(t *testing.T) {
	err := stage("artifact_finalize_failed", errors.New("disk full"))
	if got := FailureDetail(err); got != "artifact_finalize_failed" {
		t.Fatalf("FailureDetail() = %q", got)
	}
	if got := FailureDetail(operational("complete_failed", err)); got != "artifact_finalize_failed" {
		t.Fatalf("FailureDetail(operational) = %q", got)
	}
	if got := FailureDetail(NewDeferredError(time.Now(), err)); got != "artifact_finalize_failed" {
		t.Fatalf("FailureDetail(deferred) = %q", got)
	}
}

func TestFailureDetailIncludesChunkSequenceDiagnostics(t *testing.T) {
	err := stage("chunk_sequence_invalid", errors.New("invalid chunk sequence previous_order=1 order=2 previous_token_start=10 previous_token_end=20 token_start=25 token_end=30 overlap_tokens=120"))
	want := "chunk_sequence_invalid_invalid_chunk_sequence_previous_order=1_order=2_previous_token_start=10_previous_token_end=20_token_start=25_token_end=30_overlap_tokens=120"
	if got := FailureDetail(err); got != want {
		t.Fatalf("FailureDetail() = %q, want %q", got, want)
	}
}

func TestProcessorAcceptsChunkSequenceGapAtSemanticBoundary(t *testing.T) {
	first := processorTestChunk(t, 0, 0, 10)
	second := processorTestChunk(t, 1, 12, 20)
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		chunker: processorSequenceChunker{add: []domain.Chunk{first, second}},
	})
	if err := processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if repository.completes != 1 || repository.fails != 0 || writer.adds != 2 {
		t.Fatalf("gap sequence result: repo=%#v writer=%#v", repository, writer)
	}
}

func TestProcessorPersistsRetryAndFinalTimeout(t *testing.T) {
	retrying, retryRepository, retryWriter, _, _, _ := newTestProcessor(t, processorOptions{sourceErr: errors.New("source unavailable"), maximumAttempts: 2})
	err := retrying.Process(context.Background(), validProcessorEvent())
	if !errors.Is(err, ErrProcessingDeferred) || retryRepository.retries != 1 || retryRepository.fails != 0 || retryWriter.aborts != 0 {
		t.Fatalf("retry result: err=%v repo=%#v writer=%#v", err, retryRepository, retryWriter)
	}

	final, finalRepository, finalWriter, _, _, _ := newTestProcessor(t, processorOptions{waitForContext: true, maximumAttempts: 1})
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
	processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
		extractErr:      extractorErr,
		maximumAttempts: 2,
	})

	err := processor.Process(context.Background(), validProcessorEvent())
	if !errors.Is(err, ErrProcessingDeferred) || repository.retries != 1 || repository.fails != 0 || writer.aborts != 1 {
		t.Fatalf("internal extraction retry: err=%v repo=%#v writer=%#v", err, repository, writer)
	}
}

func TestProcessorPersistsRetryAfterParentCancellation(t *testing.T) {
	processor, repository, _, _, _, _ := newTestProcessor(t, processorOptions{waitForContext: true, maximumAttempts: 2})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := processor.Process(ctx, validProcessorEvent())
	if !errors.Is(err, ErrProcessingDeferred) || repository.retries != 1 || repository.retryCtxErr != nil {
		t.Fatalf("shutdown retry result: err=%v retries=%d retryCtxErr=%v", err, repository.retries, repository.retryCtxErr)
	}
}

func TestProcessorPersistsFailureAfterParentCancellation(t *testing.T) {
	processor, repository, _, _, _, _ := newTestProcessor(t, processorOptions{waitForContext: true, maximumAttempts: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := processor.Process(ctx, validProcessorEvent())
	if err != nil || repository.fails != 1 || repository.failCtxErr != nil {
		t.Fatalf("shutdown failure result: err=%v fails=%d failCtxErr=%v", err, repository.fails, repository.failCtxErr)
	}
	if repository.failedJob.Failure() != domain.FailureInternalProcessing {
		t.Fatalf("failure category = %q, want %q", repository.failedJob.Failure(), domain.FailureInternalProcessing)
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
			stagedErr := stage("chunk_page_failed", test.err)
			_, extractErr := pdfExtractor.Extract(context.Background(), "source.pdf", func(extractor.Page) error {
				return stagedErr
			})
			if got := extractorFailureDetail(extractErr); got != "chunk_page_failed" {
				t.Fatalf("extractorFailureDetail() = %q, want chunk_page_failed", got)
			}
			processor, repository, writer, _, _, _ := newTestProcessor(t, processorOptions{
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
	processor, repository, writer, _, _, pdfExtractor := newTestProcessor(t, processorOptions{extractErr: errors.New("extractor crashed"), maximumAttempts: 1})
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
	processor, repository, _, _, diagnostics, _ := newTestProcessor(t, processorOptions{completeErr: completeFailure})
	err := processor.Process(context.Background(), validProcessorEvent())
	if !errors.Is(err, completeFailure) || FailureReason(err) != "complete_failed" || repository.completes != 1 {
		t.Fatalf("completion error = %v reason=%q", err, FailureReason(err))
	}
	if diagnostics.readyPrepared != 1 || diagnostics.completionPersist != 0 || diagnostics.completionFailed != 1 || diagnostics.lastReason != "complete_failed" {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}

	processor, repository, writer, events, diagnostics, _ := newTestProcessor(t, processorOptions{readyErr: errors.New("encode ready event")})
	if err = processor.Process(context.Background(), validProcessorEvent()); err != nil {
		t.Fatal(err)
	}
	if events.ready != 1 || events.failed != 1 || repository.fails != 1 || writer.aborts != 0 {
		t.Fatalf("ready event fallback: repo=%#v writer=%#v events=%#v", repository, writer, events)
	}
	if diagnostics.readyPrepared != 0 || diagnostics.completionPersist != 0 || diagnostics.completionFailed != 0 || diagnostics.failurePersisted != 1 || diagnostics.lastDetail != "ready_event_prepare_failed" {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
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
	accepted           *bool
	acceptWait         bool
	deletionWait       bool
	sourceErr          error
	extractErr         error
	waitForContext     bool
	maximumAttempts    int
	completeErr        error
	readyErr           error
	addErr             error
	finalizeErr        error
	chunker            Chunker
	persistenceTimeout time.Duration
	selection          ContentSelectionProfile
	decodeUploaded     UploadDecoder
	decodeSelection    ContentSelectionDecoder
	selectionAccepted  bool
	selectionTimedOut  bool
	selectionJob       domain.ProcessingJob
	uploadedPayload    []byte
	pages              []extractor.Page
}

func newTestProcessor(t *testing.T, options processorOptions) (*Processor, *processorRepository, *processorWriter, *processorEvents, *processorDiagnostics, *processorExtractor) {
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
	repository := &processorRepository{
		accepted:          accepted,
		acceptWait:        options.acceptWait,
		completeErr:       options.completeErr,
		deletionWait:      options.deletionWait,
		selectionAccepted: options.selectionAccepted,
		selectionTimedOut: options.selectionTimedOut,
		selectionJob:      options.selectionJob,
		uploadedPayload:   append([]byte(nil), options.uploadedPayload...),
	}
	writer := &processorWriter{
		result:      artifact.Result{ManifestReference: "books/book-1/source/profile/manifest.pb", ManifestSHA256: sha256.Sum256([]byte("manifest")), ManifestByteSize: 8, PageCount: 1, ChunkCount: 1},
		addErr:      options.addErr,
		finalizeErr: options.finalizeErr,
	}
	events := &processorEvents{readyErr: options.readyErr}
	diagnostics := &processorDiagnostics{}
	pdfExtractor := &processorExtractor{err: options.extractErr, waitForCtx: options.waitForContext, pages: options.pages}
	extractors, err := NewFormatExtractors(ExtractionAdapter{
		MediaType: MediaTypePDF,
		Extension: ".pdf",
		Version:   extractor.ExtractionVersion,
		Extractor: pdfExtractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	persistenceTimeout := options.persistenceTimeout
	if persistenceTimeout == 0 {
		persistenceTimeout = 10 * time.Second
	}
	processor, err := NewProcessor(
		repository,
		processorSource{contents: contents, size: int64(len(contents)), err: options.sourceErr},
		extractors,
		processorFactory{writer: writer, chunker: options.chunker, selection: options.selection},
		events,
		func() (string, error) { return "job-1", nil },
		func() time.Time { return now },
		"worker-1",
		Config{
			MaximumSourceBytes:          25 << 20,
			MaximumTemporaryBytes:       25 << 20,
			TemporaryDirectory:          t.TempDir(),
			ProcessingTimeout:           10 * time.Millisecond,
			PersistenceTimeout:          persistenceTimeout,
			ArtifactAbortTimeout:        10 * time.Second,
			JobLease:                    31 * time.Second,
			ContentSelectionWaitTimeout: 15 * time.Minute,
			MaximumAttempts:             maximumAttempts,
			FirstRetryDelay:             5 * time.Second,
			SecondRetryDelay:            30 * time.Second,
			SubsequentRetryDelay:        2 * time.Minute,
			Diagnostics:                 diagnostics,
			DecodeUploaded:              options.decodeUploaded,
			DecodeContentSelection:      options.decodeSelection,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return processor, repository, writer, events, diagnostics, pdfExtractor
}

func validProcessorEvent() UploadedEvent {
	contents := []byte("%PDF-1.7\nsynthetic")
	return UploadedEvent{
		EventID:          "event-1",
		BookID:           "book-1",
		ObjectReference:  "originals/book-1.pdf",
		MediaType:        "application/pdf",
		CorrelationID:    "correlation-1",
		CausationID:      "causation-1",
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "book-1",
		SourceSHA256:     sha256.Sum256(contents),
		ByteSize:         int64(len(contents)),
		LifecycleVersion: 1,
		OccurredAt:       time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC),
		Payload:          []byte("synthetic-protobuf-payload"),
	}
}

func processorTestChunk(t *testing.T, order, tokenStart, tokenEnd uint64) domain.Chunk {
	t.Helper()
	chunk, err := domain.NewChunk(domain.ChunkInput{
		ID:         fmt.Sprintf("chunk-%d", order),
		BookID:     "book-1",
		Order:      order,
		Text:       fmt.Sprintf("chunk text %d", order),
		PageStart:  1,
		PageEnd:    1,
		TokenStart: tokenStart,
		TokenEnd:   tokenEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	return chunk
}

func validSelectionProfile(mode ContentSelectionMode) ContentSelectionProfile {
	model := sha256.Sum256([]byte("model"))
	return ContentSelectionProfile{
		Mode: mode, PolicyVersion: "layout-selector-v1", ParserVersion: "docling-serve-v1.21.0",
		ModelSHA256: hex.EncodeToString(model[:]), MinimumSignals: 2, MaximumRanges: 256, MaximumExcludedRatio: 0.25,
	}
}

func validSelectionResult(event UploadedEvent, profile ContentSelectionProfile) ContentSelectionResult {
	payload := []byte("synthetic-selection-payload")
	processingDigest := sha256.Sum256([]byte(event.MediaType))
	return ContentSelectionResult{
		EventID: "selection-result-1", RequestID: "selection-request-1", JobID: "job-1", BookID: event.BookID,
		CorrelationID: event.CorrelationID, CausationID: "selection-request-1", Producer: "ingestion-layout-worker",
		SchemaVersion: "v1", IdempotencyKey: "selection-request-1", SourceSHA256: event.SourceSHA256,
		ProcessingProfileDigest: processingDigest, PolicyDigest: profile.PolicyDigest(), PayloadDigest: sha256.Sum256(payload),
		MediaType: event.MediaType, Mode: string(profile.Mode), PolicyVersion: profile.PolicyVersion,
		ParserVersion: profile.ParserVersion, ModelSHA256: profile.ModelSHA256, FallbackReason: "none",
		LifecycleVersion: event.LifecycleVersion, OriginalLocationCount: 1, OccurredAt: event.OccurredAt.Add(time.Hour), Payload: payload,
	}
}

func processingSelectionJob(t *testing.T, event UploadedEvent) domain.ProcessingJob {
	t.Helper()
	digest := sha256.Sum256([]byte(event.MediaType))
	job, err := domain.NewProcessingJob("job-1", event.BookID, event.SourceSHA256, hex.EncodeToString(digest[:]), event.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.Claim("worker-1", event.OccurredAt.Add(time.Minute), time.Hour); err != nil {
		t.Fatal(err)
	}
	return job
}

func boolPointer(value bool) *bool { return &value }
