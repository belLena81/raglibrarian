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

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
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
