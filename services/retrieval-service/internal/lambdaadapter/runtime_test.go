package lambdaadapter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidatePrivateEndpointRejectsCredentialExfiltrationTargets(t *testing.T) {
	for _, endpoint := range []string{"https://8.8.8.8", "http://user:secret@10.0.0.1", "https://10.0.0.1?key=value"} {
		if err := validatePrivateEndpoint(context.Background(), endpoint); err == nil {
			t.Fatalf("validatePrivateEndpoint(%q) error = nil", endpoint)
		}
	}
	if err := validatePrivateEndpoint(context.Background(), "https://10.0.0.1"); err != nil {
		t.Fatalf("private endpoint rejected: %v", err)
	}
}

func TestValidatePlannerSecretRequiresLifecycleConfiguration(t *testing.T) {
	if _, err := validatePlannerSecret(Secret{
		PostgresDSN:    "postgres://retrieval_planner@postgres/retrieval",
		Region:         "us-east-1",
		ArtifactBucket: "raglibrarian-artifacts",
	}); err == nil {
		t.Fatal("validatePlannerSecret() error = nil")
	}
}

func TestValidatePlannerSecretAcceptsCompleteLifecycleConfiguration(t *testing.T) {
	configured, err := validatePlannerSecret(Secret{
		PostgresDSN:    "postgres://retrieval_planner@postgres/retrieval",
		Region:         "us-east-1",
		ArtifactBucket: "raglibrarian-artifacts",
		QdrantURL:      "https://10.0.0.2",
		QdrantAPIKey:   "qdrant-key",
	})
	if err != nil {
		t.Fatalf("validatePlannerSecret() error = %v", err)
	}
	if !configured {
		t.Fatal("complete lifecycle vector configuration was not enabled")
	}
}

func TestValidatePlannerSecretRejectsIncompleteLifecycleConfiguration(t *testing.T) {
	for _, secret := range []Secret{
		{PostgresDSN: "postgres://retrieval_planner@postgres/retrieval", Region: "us-east-1", ArtifactBucket: "raglibrarian-artifacts", QdrantURL: "https://10.0.0.2"},
		{PostgresDSN: "postgres://retrieval_planner@postgres/retrieval", Region: "us-east-1", ArtifactBucket: "raglibrarian-artifacts", QdrantAPIKey: "qdrant-key"},
	} {
		if _, err := validatePlannerSecret(secret); err == nil {
			t.Fatalf("validatePlannerSecret(%#v) error = nil", secret)
		}
	}
}

func TestPlanReturnsRuntimeErrorForLifecycleWithoutProcessor(t *testing.T) {
	runtime := &Runtime{}

	err := runtime.Plan(context.Background(), lifecycleRabbitEvent(t, validLambdaDeletionPayload(t)))
	if err == nil || err.Error() != "lifecycle processor unavailable" {
		t.Fatalf("Plan() error = %v, want lifecycle processor unavailable", err)
	}
}

func TestPlanRetriesManifestReadFailureBeforeExhaustion(t *testing.T) {
	recorder := &lambdaManifestFailureRecorder{}
	runtime := &Runtime{
		objects:       lambdaObjectStore{readErr: errors.New("artifact exceeds limit")},
		manifestFails: recorder,
	}

	err := runtime.Plan(context.Background(), manifestRabbitEvent(t, validLambdaManifestPayload(t), 3))
	if err == nil {
		t.Fatal("Plan() error = nil")
	}
	if recorder.calls != 0 {
		t.Fatalf("terminal manifest failures = %d, want 0", recorder.calls)
	}
}

func lifecycleRabbitEvent(t *testing.T, payload []byte) RabbitEvent {
	t.Helper()
	return RabbitEvent{
		Messages: map[string][]RabbitMessage{
			"retrieval.book-lifecycle.v1": {
				{
					Data: base64.StdEncoding.EncodeToString(payload),
				},
			},
		},
	}
}

func TestPlanRecordsManifestReadFailureAfterRetryExhaustion(t *testing.T) {
	recorder := &lambdaManifestFailureRecorder{}
	runtime := &Runtime{
		objects:       lambdaObjectStore{readErr: errors.New("artifact exceeds limit")},
		manifestFails: recorder,
	}

	if err := runtime.Plan(context.Background(), manifestRabbitEvent(t, validLambdaManifestPayload(t), 4)); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if recorder.calls != 1 || recorder.category != domain.FailureManifestIntegrity || recorder.event.BookID != "book-1" || recorder.event.ManifestReference == "" {
		t.Fatalf("recorded manifest failure calls=%d category=%q event=%#v", recorder.calls, recorder.category, recorder.event)
	}
	if len(recorder.event.Manifest.Shards) != 0 {
		t.Fatal("recorded read failure retained manifest artifact payload")
	}
}

func TestPlanDoesNotRecordInvalidManifestEnvelope(t *testing.T) {
	payload := validLambdaManifestPayload(t)
	var outer ingestionv1.BookChunksReadyV1
	if err := proto.Unmarshal(payload, &outer); err != nil {
		t.Fatal(err)
	}
	outer.BookId = "invalid/book"
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&outer)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &lambdaManifestFailureRecorder{}
	runtime := &Runtime{
		objects:       lambdaObjectStore{readErr: errors.New("artifact exceeds limit")},
		manifestFails: recorder,
	}

	err = runtime.Plan(context.Background(), manifestRabbitEvent(t, payload, 4))
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("Plan() error = %v, want invalid event", err)
	}
	if recorder.calls != 0 {
		t.Fatalf("terminal manifest failures = %d, want 0", recorder.calls)
	}
}

func TestIndexRecordsTerminalFailureBeforeBestEffortVectorDeactivate(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{ok: true}
	cleanup := &lambdaVectorCleanupRepository{}
	vectors := &lambdaVectorDeactivator{err: errors.New("qdrant unavailable")}
	runtime := &Runtime{
		indexer:    lambdaBatchProcessor{err: application.Failure(domain.FailureManifestIntegrity, errors.New("malformed shard"))},
		batchFails: recorder,
		vectorJobs: cleanup,
		vector:     vectors,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 4))
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if recorder.calls != 1 || recorder.category != domain.FailureManifestIntegrity || recorder.work.JobID != "job-1" {
		t.Fatalf("recorded failure calls=%d category=%q work=%#v", recorder.calls, recorder.category, recorder.work)
	}
	if vectors.calls != 1 || vectors.jobID != "job-1" {
		t.Fatalf("vector deactivate calls=%d job_id=%q", vectors.calls, vectors.jobID)
	}
	if cleanup.completed != 0 {
		t.Fatalf("completed cleanup calls=%d", cleanup.completed)
	}
}

func TestIndexSkipsVectorCleanupWhenFailureDidNotTransitionJob(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{}
	cleanup := &lambdaVectorCleanupRepository{}
	vectors := &lambdaVectorDeactivator{}
	runtime := &Runtime{
		indexer:    lambdaBatchProcessor{err: application.Failure(domain.FailureManifestIntegrity, errors.New("duplicate replay"))},
		batchFails: recorder,
		vectorJobs: cleanup,
		vector:     vectors,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 4))
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("recorded failure calls=%d", recorder.calls)
	}
	if vectors.calls != 0 || cleanup.completed != 0 {
		t.Fatalf("vector cleanup calls=%d completed=%d", vectors.calls, cleanup.completed)
	}
}

func TestIndexReturnsRecordFailureBeforeVectorDeactivate(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{err: errors.New("database unavailable")}
	vectors := &lambdaVectorDeactivator{}
	runtime := &Runtime{
		indexer:    lambdaBatchProcessor{err: application.Failure(domain.FailureResourceLimit, errors.New("too large"))},
		batchFails: recorder,
		vector:     vectors,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 4))
	if err == nil {
		t.Fatal("Index() error = nil")
	}
	if recorder.calls != 1 || vectors.calls != 0 {
		t.Fatalf("recorded calls=%d vector deactivate calls=%d", recorder.calls, vectors.calls)
	}
}

func TestIndexCompletesVectorCleanupAfterSuccessfulDeactivate(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{ok: true}
	cleanup := &lambdaVectorCleanupRepository{}
	vectors := &lambdaVectorDeactivator{}
	runtime := &Runtime{
		indexer:    lambdaBatchProcessor{err: application.Failure(domain.FailureResourceLimit, errors.New("too large"))},
		batchFails: recorder,
		vectorJobs: cleanup,
		vector:     vectors,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 4))
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if cleanup.completed != 1 || cleanup.completedJobID != "job-1" {
		t.Fatalf("completed cleanup calls=%d job_id=%q", cleanup.completed, cleanup.completedJobID)
	}
}

func TestIndexRecordsTimeoutWithFreshFailureContext(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{ok: true}
	cleanup := &lambdaVectorCleanupRepository{}
	vectors := &lambdaVectorDeactivator{}
	runtime := &Runtime{
		indexer: lambdaBatchProcessor{process: func(ctx context.Context, _ application.BatchWork) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		batchFails:         recorder,
		vectorJobs:         cleanup,
		vector:             vectors,
		processingTimeout:  time.Nanosecond,
		failureRecordLimit: time.Second,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 1))
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if recorder.calls != 1 || recorder.category != domain.FailureIndexingTimeout {
		t.Fatalf("recorded timeout calls=%d category=%q", recorder.calls, recorder.category)
	}
	if recorder.ctxErr != nil {
		t.Fatalf("failure context already expired: %v", recorder.ctxErr)
	}
	if vectors.calls != 1 || cleanup.completed != 1 {
		t.Fatalf("vector cleanup calls=%d completed=%d", vectors.calls, cleanup.completed)
	}
}

func TestIndexOverridesSanitizedDependencyTimeoutToIndexingTimeout(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{ok: true}
	cleanup := &lambdaVectorCleanupRepository{}
	vectors := &lambdaVectorDeactivator{}
	runtime := &Runtime{
		indexer: lambdaBatchProcessor{process: func(ctx context.Context, _ application.BatchWork) error {
			<-ctx.Done()
			return application.Failure(domain.FailureEmbeddingUnavailable, errors.New("embed shard"))
		}},
		batchFails:         recorder,
		vectorJobs:         cleanup,
		vector:             vectors,
		processingTimeout:  time.Nanosecond,
		failureRecordLimit: time.Second,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 1))
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if recorder.calls != 1 || recorder.category != domain.FailureIndexingTimeout {
		t.Fatalf("recorded timeout calls=%d category=%q", recorder.calls, recorder.category)
	}
	if vectors.calls != 1 || cleanup.completed != 1 {
		t.Fatalf("vector cleanup calls=%d completed=%d", vectors.calls, cleanup.completed)
	}
}

func TestIndexPreservesDependencyFailureCategoryWithoutTimeout(t *testing.T) {
	recorder := &lambdaBatchFailureRecorder{ok: true}
	runtime := &Runtime{
		indexer:            lambdaBatchProcessor{err: application.Failure(domain.FailureEmbeddingUnavailable, errors.New("embed shard"))},
		batchFails:         recorder,
		processingTimeout:  time.Second,
		failureRecordLimit: time.Second,
	}

	err := runtime.Index(context.Background(), batchRabbitEvent(t, validLambdaBatchPayload(t), 4))
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if recorder.calls != 1 || recorder.category != domain.FailureEmbeddingUnavailable {
		t.Fatalf("recorded calls=%d category=%q", recorder.calls, recorder.category)
	}
}

func TestRetrievalProcessingTimeoutRejectsInvalidValue(t *testing.T) {
	t.Setenv("RETRIEVAL_PROCESSING_TIMEOUT", "30s")

	if _, err := retrievalProcessingTimeout(); err == nil {
		t.Fatal("retrievalProcessingTimeout() error = nil")
	}
}

func TestRetrievalProcessingTimeoutUsesDefault(t *testing.T) {
	t.Setenv("RETRIEVAL_PROCESSING_TIMEOUT", "")

	value, err := retrievalProcessingTimeout()
	if err != nil {
		t.Fatalf("retrievalProcessingTimeout() error = %v", err)
	}
	if value != defaultProcessingTimeout {
		t.Fatalf("retrievalProcessingTimeout() = %v, want %v", value, defaultProcessingTimeout)
	}
}

func TestCleanupRetriesFailedJobsAndCompletesSuccessfulOnes(t *testing.T) {
	cleanup := &lambdaVectorCleanupRepository{
		jobs: []repository.VectorCleanupJob{
			{JobID: "job-1", BookID: "book-1"},
			{JobID: "job-2", BookID: "book-2"},
		},
	}
	vectors := &lambdaVectorDeactivator{failures: map[string]error{"job-1": errors.New("qdrant unavailable")}}
	runtime := &Runtime{vectorJobs: cleanup, vector: vectors}
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)

	err := runtime.retryPendingVectorCleanup(context.Background(), now, 64)
	if err != nil {
		t.Fatalf("retryPendingVectorCleanup() error = %v", err)
	}
	if cleanup.retried != 1 || cleanup.retriedJobID != "job-1" {
		t.Fatalf("retried cleanup calls=%d job_id=%q", cleanup.retried, cleanup.retriedJobID)
	}
	if cleanup.completed != 1 || cleanup.completedJobID != "job-2" {
		t.Fatalf("completed cleanup calls=%d job_id=%q", cleanup.completed, cleanup.completedJobID)
	}
}

type lambdaObjectStore struct {
	payload []byte
	readErr error
}

func (s lambdaObjectStore) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (s lambdaObjectStore) ReadBounded(context.Context, string, int64) ([]byte, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return append([]byte(nil), s.payload...), nil
}

type lambdaManifestFailureRecorder struct {
	calls    int
	event    application.ManifestEvent
	category domain.FailureCategory
}

func (s *lambdaManifestFailureRecorder) FailManifest(_ context.Context, event application.ManifestEvent, category domain.FailureCategory, _ time.Time) error {
	s.calls++
	s.event = event
	s.category = category
	return nil
}

type lambdaBatchProcessor struct {
	err     error
	process func(context.Context, application.BatchWork) error
}

func (s lambdaBatchProcessor) Process(ctx context.Context, work application.BatchWork) error {
	if s.process != nil {
		return s.process(ctx, work)
	}
	return s.err
}

type lambdaBatchFailureRecorder struct {
	calls    int
	work     application.BatchWork
	category domain.FailureCategory
	ok       bool
	err      error
	ctxErr   error
}

func (s *lambdaBatchFailureRecorder) FailBatch(ctx context.Context, work application.BatchWork, category domain.FailureCategory, _ time.Time) (bool, error) {
	s.calls++
	s.work = work
	s.category = category
	s.ctxErr = ctx.Err()
	return s.ok, s.err
}

type lambdaVectorDeactivator struct {
	calls    int
	jobID    string
	err      error
	failures map[string]error
}

func (s *lambdaVectorDeactivator) DeactivateJob(_ context.Context, jobID string) error {
	s.calls++
	s.jobID = jobID
	if s.failures != nil {
		if err := s.failures[jobID]; err != nil {
			return err
		}
	}
	return s.err
}

func (s *lambdaVectorDeactivator) DeleteJob(ctx context.Context, jobID string) error {
	return s.DeactivateJob(ctx, jobID)
}

type lambdaVectorCleanupRepository struct {
	jobs           []repository.VectorCleanupJob
	completed      int
	completedJobID string
	retried        int
	retriedJobID   string
}

func (s *lambdaVectorCleanupRepository) PendingVectorCleanup(context.Context, int, time.Time) ([]repository.VectorCleanupJob, error) {
	return append([]repository.VectorCleanupJob(nil), s.jobs...), nil
}

func (s *lambdaVectorCleanupRepository) CompleteVectorCleanup(_ context.Context, jobID string) error {
	s.completed++
	s.completedJobID = jobID
	return nil
}

func (s *lambdaVectorCleanupRepository) RetryVectorCleanup(_ context.Context, jobID string, _ time.Time) error {
	s.retried++
	s.retriedJobID = jobID
	return nil
}

func manifestRabbitEvent(t *testing.T, payload []byte, attempt int64) RabbitEvent {
	t.Helper()
	return RabbitEvent{
		Messages: map[string][]RabbitMessage{
			"retrieval.chunks-ready.v1": {
				{
					Data: base64.StdEncoding.EncodeToString(payload),
					BasicProperties: RabbitBasicProperties{
						Headers: map[string]any{"x-delivery-count": attempt},
					},
				},
			},
		},
	}
}

func batchRabbitEvent(t *testing.T, payload []byte, attempt int64) RabbitEvent {
	t.Helper()
	return RabbitEvent{
		Messages: map[string][]RabbitMessage{
			"retrieval.index-batch.v1": {
				{
					Data: base64.StdEncoding.EncodeToString(payload),
					BasicProperties: RabbitBasicProperties{
						Headers: map[string]any{"x-delivery-count": attempt},
					},
				},
			},
		},
	}
}

func validLambdaManifestPayload(t *testing.T) []byte {
	t.Helper()
	source := sha256.Sum256([]byte("synthetic source"))
	processing := sha256.Sum256([]byte("processing profile"))
	manifest := &ingestionv1.ChunkManifestV1{
		SchemaVersion:          "v1",
		BookId:                 "book-1",
		SourceSha256:           source[:],
		ProcessingConfigDigest: processing[:],
		ExtractionVersion:      "poppler-layout-v1",
		NormalizationVersion:   "nfc-v1",
		TokenizerVersion:       "cl100k_base-v1",
		ChunkingVersion:        "token-window-v2",
		StructureVersion:       "heading-carry-v1",
		MaximumTokens:          800,
		OverlapTokens:          120,
		PageCount:              1,
		ChunkCount:             1,
		GeneratedAt:            timestamppb.New(time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)),
	}
	manifestPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestPayload)
	directory := "books/book-1/" + hex.EncodeToString(source[:]) + "/" + hex.EncodeToString(processing[:]) + "/"
	outer := &ingestionv1.BookChunksReadyV1{
		EventId:              "event-1",
		BookId:               "book-1",
		SourceSha256:         source[:],
		ManifestReference:    directory + "manifest.pb",
		ManifestSha256:       manifestDigest[:],
		ManifestByteSize:     int64(len(manifestPayload)),
		PageCount:            1,
		ChunkCount:           1,
		ExtractionVersion:    manifest.ExtractionVersion,
		NormalizationVersion: manifest.NormalizationVersion,
		TokenizerVersion:     manifest.TokenizerVersion,
		ChunkingVersion:      manifest.ChunkingVersion,
		StructureVersion:     manifest.StructureVersion,
		MaximumTokens:        manifest.MaximumTokens,
		OverlapTokens:        manifest.OverlapTokens,
		CorrelationId:        "correlation-1",
		OccurredAt:           timestamppb.New(time.Date(2026, 7, 20, 9, 1, 0, 0, time.UTC)),
		CausationId:          "cause-1",
		Producer:             "ingestion-service",
		SchemaVersion:        "v1",
		IdempotencyKey:       "book-1:" + hex.EncodeToString(processing[:]) + ":ready",
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func validLambdaBatchPayload(t *testing.T) []byte {
	t.Helper()
	source := sha256.Sum256([]byte("synthetic source"))
	manifest := sha256.Sum256([]byte("synthetic manifest"))
	shard := sha256.Sum256([]byte("synthetic shard"))
	profile := domain.SupportedIndexProfile()
	message := &retrievalv1.IndexBatchRequestedV1{
		EventId:              "batch-event-1",
		JobId:                "job-1",
		BatchId:              "job-1:0",
		BookId:               "book-1",
		ShardReference:       "books/book-1/profile/shards/000000.pb.zst",
		ShardSha256:          shard[:],
		CompressedByteSize:   128,
		UncompressedByteSize: 512,
		ChunkCount:           1,
		SourceSha256:         source[:],
		ManifestSha256:       manifest[:],
		IndexProfileDigest:   profile.Digest[:],
		FirstChunkOrder:      0,
		LastChunkOrder:       0,
		ManifestPageCount:    1,
		ExtractionVersion:    profile.ExtractionVersion,
		NormalizationVersion: profile.NormalizationVersion,
		TokenizerVersion:     profile.TokenizerVersion,
		ChunkingVersion:      profile.ChunkingVersion,
		StructureVersion:     profile.StructureVersion,
		MaximumTokens:        uint32(profile.MaximumTokens),
		OverlapTokens:        uint32(profile.OverlapTokens),
		CorrelationId:        "correlation-1",
		OccurredAt:           timestamppb.New(time.Date(2026, 7, 20, 9, 2, 0, 0, time.UTC)),
		CausationId:          "manifest-event-1",
		Producer:             "retrieval-service",
		SchemaVersion:        "v1",
		IdempotencyKey:       "job-1:0",
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func validLambdaDeletionPayload(t *testing.T) []byte {
	t.Helper()
	message := &catalogv1.BookDeletionRequestedV1{
		EventId:          "delete-event",
		BookId:           "book-1",
		CommandId:        "delete-command",
		LifecycleVersion: 2,
		ActorId:          "actor-1",
		CorrelationId:    "correlation-1",
		OccurredAt:       timestamppb.New(time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)),
		CausationId:      "delete-command",
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "delete-command",
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
