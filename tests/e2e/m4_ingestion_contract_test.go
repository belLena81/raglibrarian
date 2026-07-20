//go:build e2e && m4

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	m4MaxFixtureBytes  = 64 << 20
	m4V1MaxSourceBytes = 25 << 20
)

const (
	m4SLOProfile                 = "m4-slo-v1"
	m4ExtractingVisibilitySLO    = 2 * time.Second
	m4ReadyPropagationSLO        = time.Second
	m4TinyDocumentTerminalSLO    = 10 * time.Second
	m4AverageDocumentTerminalSLO = 120 * time.Second
)

type m4Environment struct {
	accessToken  string
	fixtureDir   string
	edgeURLs     []string
	publicOrigin string
	timeout      time.Duration
}

type m4ArtifactEnvironment struct {
	dsn       string
	endpoint  string
	accessKey string
	secretKey string
	bucket    string
	secure    bool
	caFile    string
}

type m4ArtifactReceipt struct {
	reference string
	sha256    []byte
	byteSize  int64
}

type m4Book struct {
	ID                        string    `json:"id"`
	ProcessingStatus          string    `json:"processing_status"`
	ProcessingStage           string    `json:"processing_stage"`
	ProcessingFailureCategory string    `json:"processing_failure_category"`
	ProcessingVersion         int64     `json:"processing_version"`
	ProcessingUpdatedAt       time.Time `json:"processing_updated_at"`
}

type m4SSEEvent struct {
	id        string
	eventType string
	data      []byte
}

type m4EventStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
}

type m4SSEPayload struct {
	BookID                    string    `json:"book_id"`
	ProcessingStatus          string    `json:"processing_status"`
	ProcessingStage           string    `json:"processing_stage"`
	ProcessingFailureCategory string    `json:"processing_failure_category"`
	ProcessingVersion         int64     `json:"processing_version"`
	UpdatedAt                 time.Time `json:"updated_at"`
	SchemaVersion             int       `json:"schema_version"`
}

func TestM4SyntheticPDFCorpusReachesDeterministicStatus(t *testing.T) {
	environment := loadM4Environment(t, false)
	tests := []struct {
		fixture     string
		wantStatus  string
		wantStage   string
		wantFailure string
	}{
		{fixture: "minimal.pdf", wantStatus: "processing", wantStage: "chunks_ready"},
		{fixture: "multipage.pdf", wantStatus: "processing", wantStage: "chunks_ready"},
		{fixture: "blank_middle_page.pdf", wantStatus: "processing", wantStage: "chunks_ready"},
		{fixture: "image_only.pdf", wantStatus: "failed", wantStage: "failed", wantFailure: "no_extractable_text"},
		{fixture: "encrypted.pdf", wantStatus: "failed", wantStage: "failed", wantFailure: "encrypted_document"},
		{fixture: "malformed.pdf", wantStatus: "failed", wantStage: "failed", wantFailure: "malformed_document"},
	}

	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, test.fixture)
			book = waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
				return current.ProcessingStatus == test.wantStatus && current.ProcessingStage == test.wantStage
			})

			assert.Equal(t, test.wantFailure, book.ProcessingFailureCategory)
			assert.Positive(t, book.ProcessingVersion)
			assert.False(t, book.ProcessingUpdatedAt.IsZero())
		})
	}
}

func TestM4OversizePDFIsRejectedAtUploadBoundary(t *testing.T) {
	environment := loadM4Environment(t, false)
	status := uploadM4FixtureExpectingStatus(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "oversize.pdf")
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge}, status)
}

func TestM4MaximumSourceBytesReachesAStableTerminalProjection(t *testing.T) {
	environment := loadM4Environment(t, false)
	fixtureDir := t.TempDir()
	writeM4MaximumSourceFixture(t, fixtureDir)
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, fixtureDir, "maximum_source_bytes.pdf")
	book = waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
		return current.ProcessingStage == "chunks_ready"
	})
	assert.Equal(t, "processing", book.ProcessingStatus)
	assert.Empty(t, book.ProcessingFailureCategory)
}

func TestM4PerformanceSLOProfile(t *testing.T) {
	environment := loadM4Environment(t, false)
	if configured := strings.TrimSpace(os.Getenv("M4_PERFORMANCE_PROFILE")); configured != "" && configured != m4SLOProfile {
		t.Fatalf("M4_PERFORMANCE_PROFILE must be %s", m4SLOProfile)
	}
	fixtures := []string{"minimal.pdf", "minimal.pdf", "multipage.pdf", "minimal.pdf", "multipage.pdf"}
	durationResults := make(chan time.Duration, len(fixtures))
	processingSlots := make(chan struct{}, 2)
	var wait sync.WaitGroup
	for index, fixture := range fixtures {
		wait.Add(1)
		go func() {
			defer wait.Done()
			processingSlots <- struct{}{}
			defer func() { <-processingSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), environment.timeout)
			defer cancel()
			edgeURL := environment.edgeURLs[index%len(environment.edgeURLs)]
			stream := openM4EventStream(t, ctx, edgeURL, environment.publicOrigin, environment.accessToken, "")
			startedAt := time.Now()
			book := uploadM4Fixture(t, edgeURL, environment.accessToken, environment.fixtureDir, fixture)
			waitForM4SLOEvents(t, stream, book.ID, startedAt)
			book = getM4Book(t, edgeURL, environment.accessToken, book.ID)
			assert.Equal(t, "processing", book.ProcessingStatus)
			assert.Equal(t, "chunks_ready", book.ProcessingStage)
			durationResults <- time.Since(startedAt)
		}()
	}
	wait.Wait()
	close(durationResults)
	durations := make([]time.Duration, 0, len(fixtures))
	for duration := range durationResults {
		durations = append(durations, duration)
	}
	require.Len(t, durations, len(fixtures))
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p95Index := (95*len(sorted)+99)/100 - 1
	assert.LessOrEqual(t, sorted[p95Index], m4TinyDocumentTerminalSLO, "%s tiny-document p95 exceeded", m4SLOProfile)
	var total time.Duration
	for _, duration := range durations {
		total += duration
	}
	assert.Less(t, total/time.Duration(len(durations)), m4AverageDocumentTerminalSLO, "%s mean ingestion exceeded", m4SLOProfile)
}

func TestM4ArtifactsContainValidManifestAndOrderedShards(t *testing.T) {
	environment := loadM4Environment(t, false)
	requireM4ArtifactReader(t)
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "multipage.pdf")
	waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
		return current.ProcessingStage == "chunks_ready"
	})

	manifestBytes := fetchM4Manifest(t, book.ID)
	manifest := decodeM4Manifest(t, manifestBytes, book.ID)
	chunks := fetchM4Chunks(t, manifest)
	require.Len(t, chunks, int(manifest.ChunkCount))
	for index, chunk := range chunks {
		assert.Equal(t, uint64(index), chunk.Order)
		assert.Equal(t, book.ID, chunk.BookId)
		assert.NotEmpty(t, chunk.ChunkId)
		assert.NotEmpty(t, chunk.Text)
		assert.Len(t, chunk.ContentSha256, sha256.Size)
		assert.LessOrEqual(t, chunk.PageStart, chunk.PageEnd)
		assert.LessOrEqual(t, chunk.TokenStart, chunk.TokenEnd)
		assert.NotEmpty(t, chunk.StructureVersion)
	}
	assertCrossPageStructure(t, chunks)
}

func TestM4RetryReplayKeepsManifestByteIdentical(t *testing.T) {
	environment := loadM4Environment(t, false)
	requireM4ArtifactReader(t)
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "multipage.pdf")
	waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
		return current.ProcessingStage == "chunks_ready"
	})
	before := fetchM4Manifest(t, book.ID)
	payload, eventID := fetchM4AcceptedEnvelope(t, book.ID)
	publishM4UploadedEnvelope(t, eventID, payload)
	after := fetchM4Manifest(t, book.ID)
	assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after), "retry changed deterministic manifest bytes")
}

func TestM4PoisonEnvelopeDoesNotBlockFollowingUpload(t *testing.T) {
	environment := loadM4Environment(t, false)
	eventID := randomM4MessageID(t, "m4-poison-")
	payload := []byte("m4 synthetic malformed protobuf")
	publishM4UploadedEnvelope(t, eventID, payload)
	poison := waitForM4DeadLetter(t, eventID, 30*time.Second)
	assertM4DeadLetterConfidentiality(t, poison, payload)
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "minimal.pdf")
	book = waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
		return current.ProcessingStage == "chunks_ready"
	})
	assert.Equal(t, "processing", book.ProcessingStatus)
	assert.Empty(t, book.ProcessingFailureCategory)
}

func TestM4DeadLetterDiagnosticScannerFindsNestedSensitiveValues(t *testing.T) {
	forbidden := [][]byte{[]byte("%pdf-"), []byte("m4_canary_"), []byte("postgres://")}
	safe := amqp091.Table{"x-death": []any{amqp091.Table{"reason": "rejected", "queue": "ingestion.book-uploaded.v1"}}}
	assert.False(t, m4TableContainsForbiddenDiagnostic(safe, forbidden))
	exposed := amqp091.Table{"diagnostic": []any{amqp091.Table{"detail": "M4_CANARY_SYNTHETIC"}}}
	assert.True(t, m4TableContainsForbiddenDiagnostic(exposed, forbidden))
}

func TestM4DeadLetterMismatchUsesContentFreeDiagnosticAfterScanning(t *testing.T) {
	delivery := amqp091.Delivery{Body: []byte{0xde, 0xad, 0xbe, 0xef}}
	failure := m4DeadLetterConfidentialityFailure(delivery, []byte("expected bounded poison"))
	assert.Equal(t, "M4 dead-letter body did not match the bounded poison envelope", failure)
	assert.NotContains(t, failure, string(delivery.Body))

	delivery.Body = []byte("%PDF-private synthetic bytes")
	failure = m4DeadLetterConfidentialityFailure(delivery, []byte("different expected value"))
	assert.Equal(t, "M4 dead-letter message exposed raw PDF content, a canary, or sensitive diagnostics", failure)
}

func TestM4RecoveryMarkerAcceptsOnlyCanonicalBookIDs(t *testing.T) {
	assert.True(t, isCanonicalM4BookID("00000000-0000-4000-8000-000000000001"))
	assert.False(t, isCanonicalM4BookID("00000000-0000-4000-8000-000000000001\nworker-restarted"))
	assert.False(t, isCanonicalM4BookID("00000000-0000-4000-8000-00000000000G"))
}

func TestM4RecoveryMarkerHandshakeUsesPrivateAtomicFiles(t *testing.T) {
	controlDir := t.TempDir()
	require.NoError(t, os.Chmod(controlDir, 0o700))
	uploadAccepted := filepath.Join(controlDir, "upload-accepted")
	bookID := "00000000-0000-4000-8000-000000000001"
	writeM4RecoveryMarker(t, controlDir, uploadAccepted, []byte(bookID+"\n"))
	info, err := os.Lstat(uploadAccepted)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	contents, err := os.ReadFile(uploadAccepted) // #nosec G304 -- test-owned temporary marker.
	require.NoError(t, err)
	assert.Equal(t, bookID+"\n", string(contents))

	workerRestarted := filepath.Join(controlDir, "worker-restarted")
	writeM4RecoveryMarker(t, controlDir, workerRestarted, nil)
	waitForM4WorkerRestarted(t, controlDir, workerRestarted, time.Second)
}

func TestM4WorkerDownRecovery(t *testing.T) {
	controlDir := loadM4RecoveryControlDir(t)
	uploadAccepted := filepath.Join(controlDir, "upload-accepted")
	workerRestarted := filepath.Join(controlDir, "worker-restarted")
	removeM4RecoveryMarker(t, uploadAccepted)
	removeM4RecoveryMarker(t, workerRestarted)
	defer removeM4RecoveryMarker(t, uploadAccepted)
	defer removeM4RecoveryMarker(t, workerRestarted)

	environment := loadM4Environment(t, false)
	requireM4ArtifactReader(t)
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "minimal.pdf")
	if !isCanonicalM4BookID(book.ID) {
		t.Fatal("M4 recovery upload returned a non-canonical book ID")
	}
	writeM4RecoveryMarker(t, controlDir, uploadAccepted, []byte(book.ID+"\n"))
	waitForM4WorkerRestarted(t, controlDir, workerRestarted, environment.timeout)

	book = waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
		return current.ProcessingStage == "chunks_ready"
	})
	assert.Equal(t, "processing", book.ProcessingStatus)
	assert.Empty(t, book.ProcessingFailureCategory)
	assertM4SingularDurableState(t, book.ID)
	first := fetchM4Manifest(t, book.ID)
	second := fetchM4Manifest(t, book.ID)
	assert.Equal(t, sha256.Sum256(first), sha256.Sum256(second), "recovery changed deterministic manifest bytes")
}

func TestM4CanaryIsConfinedToPrivilegedArtifacts(t *testing.T) {
	environment := loadM4Environment(t, false)
	requireM4ArtifactReader(t)
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "canary.pdf")
	book = waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
		return current.ProcessingStage == "chunks_ready"
	})
	publicProjection, err := json.Marshal(book)
	require.NoError(t, err)
	if bytes.Contains(publicProjection, []byte("M4_CANARY_")) {
		t.Fatal("public projection exposed the artifact-confidentiality canary")
	}
	chunks := fetchM4Chunks(t, decodeM4Manifest(t, fetchM4Manifest(t, book.ID), book.ID))
	found := false
	for _, chunk := range chunks {
		found = found || strings.Contains(chunk.Text, "M4_CANARY_")
	}
	if !found {
		t.Fatal("privileged artifact reader did not find the expected synthetic canary")
	}
}

func TestM4KnownV1WireContractAcceptsUnknownAdditiveProtobufField(t *testing.T) {
	event := &catalogv1.BookUploadedV1{
		EventId:         "00000000-0000-4000-8000-000000000004",
		BookId:          "00000000-0000-4000-8000-000000000005",
		ObjectReference: "originals/00000000-0000-4000-8000-000000000005.pdf",
		Sha256:          bytes.Repeat([]byte{0x4a}, sha256.Size),
		ByteSize:        42,
		MediaType:       "application/pdf",
		CorrelationId:   "00000000-0000-4000-8000-000000000006",
		CausationId:     "00000000-0000-4000-8000-000000000007",
		Producer:        "catalog-service", SchemaVersion: "v1",
		IdempotencyKey: "00000000-0000-4000-8000-000000000005",
		OccurredAt:     timestamppb.Now(),
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	require.NoError(t, err)
	payload = protowire.AppendTag(payload, 127, protowire.BytesType)
	payload = protowire.AppendString(payload, "future-additive-value")
	var decoded catalogv1.BookUploadedV1
	require.NoError(t, (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &decoded))
	assert.Equal(t, event.BookId, decoded.BookId)
	assert.NotEmpty(t, decoded.ProtoReflect().GetUnknown())
}

func TestM4SSERequiresBearerAuthenticationAndStartsWithResync(t *testing.T) {
	environment := loadM4Environment(t, false)

	request, err := http.NewRequest(http.MethodGet, environment.edgeURLs[0]+"/books/events", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", environment.publicOrigin)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := openM4EventStream(t, ctx, environment.edgeURLs[0], environment.publicOrigin, environment.accessToken, "")
	event, err := readM4SSEEvent(stream)
	require.NoError(t, err)
	assert.Equal(t, "books-resync", event.eventType)
}

func TestM4SSEClosesAfterSessionRevocation(t *testing.T) {
	environment := loadM4Environment(t, false)
	revocableToken := loadM4Token(t, "M4_E2E_REVOCABLE_ACCESS_TOKEN", "M4_E2E_REVOCABLE_ACCESS_TOKEN_FILE")
	if revocableToken == "" {
		t.Fatal("M4_E2E_REVOCABLE_ACCESS_TOKEN or its file variant is required and must belong to a disposable active session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stream := openM4EventStream(t, ctx, environment.edgeURLs[0], environment.publicOrigin, revocableToken, "")
	event, err := readM4SSEEvent(stream)
	require.NoError(t, err)
	require.Equal(t, "books-resync", event.eventType)

	logoutRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, environment.edgeURLs[0]+"/auth/logout", nil)
	require.NoError(t, err)
	logoutRequest.Header.Set("Authorization", "Bearer "+revocableToken)
	logoutRequest.Header.Set("Origin", environment.publicOrigin)
	logoutResponse, err := http.DefaultClient.Do(logoutRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, logoutResponse.StatusCode)
	require.NoError(t, logoutResponse.Body.Close())

	closed := make(chan error, 1)
	go func() {
		for {
			if _, readErr := readM4SSEEvent(stream); readErr != nil {
				closed <- readErr
				return
			}
		}
	}()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("SSE stream remained open beyond the session revalidation window")
	}

	rejectedRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, environment.edgeURLs[0]+"/books/events", nil)
	require.NoError(t, err)
	rejectedRequest.Header.Set("Accept", "text/event-stream")
	rejectedRequest.Header.Set("Authorization", "Bearer "+revocableToken)
	rejectedRequest.Header.Set("Origin", environment.publicOrigin)
	rejectedResponse, err := http.DefaultClient.Do(rejectedRequest)
	require.NoError(t, err)
	defer rejectedResponse.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, rejectedResponse.StatusCode)
}

func TestM4StatusEventsFanOutAndReconcileAcrossEdgeInstances(t *testing.T) {
	environment := loadM4Environment(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), environment.timeout)
	defer cancel()

	streams := make([]*m4EventStream, 0, len(environment.edgeURLs))
	for _, edgeURL := range environment.edgeURLs {
		streams = append(streams, openM4EventStream(t, ctx, edgeURL, environment.publicOrigin, environment.accessToken, ""))
	}
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "minimal.pdf")

	for index, stream := range streams {
		payload, eventID := waitForM4ReadyEvent(t, stream, book.ID)
		assert.NotEmpty(t, eventID, "Edge instance %d omitted the Catalog event ID", index)
		assert.Equal(t, "processing", payload.ProcessingStatus)
		assert.Equal(t, "chunks_ready", payload.ProcessingStage)
		assert.Empty(t, payload.ProcessingFailureCategory)
		assert.Positive(t, payload.ProcessingVersion)
		assert.Equal(t, 1, payload.SchemaVersion)
		assert.False(t, payload.UpdatedAt.IsZero())
	}

	var authoritative m4Book
	for _, edgeURL := range environment.edgeURLs {
		current := getM4Book(t, edgeURL, environment.accessToken, book.ID)
		if authoritative.ID == "" {
			authoritative = current
			continue
		}
		assert.Equal(t, authoritative.ProcessingStatus, current.ProcessingStatus)
		assert.Equal(t, authoritative.ProcessingStage, current.ProcessingStage)
		assert.Equal(t, authoritative.ProcessingVersion, current.ProcessingVersion)
	}
	for _, stream := range streams {
		require.NoError(t, stream.body.Close())
	}

	reconnectCtx, reconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reconnectCancel()
	reconnected := openM4EventStream(t, reconnectCtx, environment.edgeURLs[0], environment.publicOrigin, environment.accessToken, "missed-event")
	event, err := readM4SSEEvent(reconnected)
	require.NoError(t, err)
	assert.Equal(t, "books-resync", event.eventType, "missed events must trigger authoritative reconciliation")
	reconciled := getM4Book(t, environment.edgeURLs[0], environment.accessToken, book.ID)
	assert.Equal(t, authoritative.ProcessingVersion, reconciled.ProcessingVersion)
}

func loadM4Environment(t *testing.T, requireFanout bool) m4Environment {
	t.Helper()
	environment := m4Environment{
		accessToken:  loadM4Token(t, "M4_E2E_ACCESS_TOKEN", "M4_E2E_ACCESS_TOKEN_FILE"),
		fixtureDir:   strings.TrimSpace(os.Getenv("M4_E2E_FIXTURE_DIR")),
		publicOrigin: strings.TrimRight(strings.TrimSpace(os.Getenv("M4_E2E_PUBLIC_ORIGIN")), "/"),
		timeout:      2 * time.Minute,
	}
	if environment.accessToken == "" {
		t.Fatal("M4_E2E_ACCESS_TOKEN or M4_E2E_ACCESS_TOKEN_FILE is required")
	}
	if environment.fixtureDir == "" {
		t.Fatal("M4_E2E_FIXTURE_DIR is required; generate it with tests/fixtures/ingestion/generate.go")
	}
	origin, err := url.Parse(environment.publicOrigin)
	if environment.publicOrigin == "" || err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("M4_E2E_PUBLIC_ORIGIN must be an absolute HTTP(S) origin")
	}
	if value := strings.TrimSpace(os.Getenv("M4_E2E_STATUS_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second || parsed > 15*time.Minute {
			t.Fatal("M4_E2E_STATUS_TIMEOUT must be between 1s and 15m")
		}
		environment.timeout = parsed
	}
	for _, value := range strings.Split(os.Getenv("M4_E2E_EDGE_BASE_URLS"), ",") {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("M4_E2E_EDGE_BASE_URLS contains an invalid base URL")
		}
		environment.edgeURLs = append(environment.edgeURLs, value)
	}
	if len(environment.edgeURLs) == 0 {
		t.Fatal("M4_E2E_EDGE_BASE_URLS is required")
	}
	if requireFanout && len(environment.edgeURLs) < 2 {
		t.Fatal("M4_E2E_EDGE_BASE_URLS must contain at least two Edge instances for the fanout test")
	}
	return environment
}

func loadM4Token(t *testing.T, directKey, fileKey string) string {
	t.Helper()
	if direct := strings.TrimSpace(os.Getenv(directKey)); direct != "" {
		return direct
	}
	return loadM4FileSecret(t, fileKey)
}

func loadM4FileSecret(t *testing.T, fileKey string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(fileKey))
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must be an absolute path", fileKey)
	}
	return readM4SecretFile(t, path, 16<<10)
}

func uploadM4Fixture(t *testing.T, edgeURL, token, fixtureDir, fixtureName string) m4Book {
	t.Helper()
	fixture, err := os.Open(filepath.Join(fixtureDir, fixtureName)) // #nosec G304 -- fixtureName is a test-owned constant.
	require.NoError(t, err)
	defer fixture.Close()
	info, err := fixture.Stat()
	require.NoError(t, err)
	require.Positive(t, info.Size())
	require.LessOrEqual(t, info.Size(), int64(m4MaxFixtureBytes))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := textproto.MIMEHeader{}
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadata, err := writer.CreatePart(metadataHeader)
	require.NoError(t, err)
	_, err = fmt.Fprintf(metadata, `{"title":"M4 synthetic %s","author":"RAGLibrarian QA","year":2026,"tags":["m4-synthetic"]}`, fixtureName)
	require.NoError(t, err)
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%s`, strconv.Quote(fixtureName)))
	fileHeader.Set("Content-Type", "application/pdf")
	file, err := writer.CreatePart(fileHeader)
	require.NoError(t, err)
	_, err = io.Copy(file, io.LimitReader(fixture, m4MaxFixtureBytes+1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, edgeURL+"/books", &body)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", loadM4PublicOrigin(t))
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var book m4Book
	decodeJSON(t, response, &book)
	require.NotEmpty(t, book.ID)
	return book
}

func writeM4MaximumSourceFixture(t *testing.T, destinationDir string) {
	t.Helper()
	padding := m4V1MaxSourceBytes
	var contents []byte
	for attempts := 0; attempts < 4; attempts++ {
		contents = buildM4BoundaryPDF(padding)
		padding += m4V1MaxSourceBytes - len(contents)
	}
	require.Len(t, contents, m4V1MaxSourceBytes)
	require.NoError(t, os.WriteFile(filepath.Join(destinationDir, "maximum_source_bytes.pdf"), contents, 0o600))
}

func buildM4BoundaryPDF(padding int) []byte {
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n")
	offsets := make([]int, 6)
	writeObject := func(id int, value string) {
		offsets[id] = output.Len()
		_, _ = fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", id, value)
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Count 1 /Kids [4 0 R] >>")
	writeObject(3, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	writeObject(4, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>")
	content := "BT /F1 12 Tf 72 720 Td (Synthetic maximum source boundary.) Tj ET\n" + strings.Repeat(" ", padding)
	writeObject(5, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	xref := output.Len()
	output.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		_, _ = fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	_, _ = fmt.Fprintf(&output, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return output.Bytes()
}

func uploadM4FixtureExpectingStatus(t *testing.T, edgeURL, token, fixtureDir, fixtureName string) int {
	t.Helper()
	fixture, err := os.Open(filepath.Join(fixtureDir, fixtureName)) // #nosec G304 -- fixtureName is a test-owned constant.
	require.NoError(t, err)
	defer fixture.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := writer.CreateFormField("metadata")
	require.NoError(t, err)
	_, err = io.WriteString(metadata, `{"title":"M4 synthetic oversize","author":"RAGLibrarian QA","year":2026,"tags":["m4-synthetic"]}`)
	require.NoError(t, err)
	file, err := writer.CreateFormFile("file", fixtureName)
	require.NoError(t, err)
	_, err = io.Copy(file, fixture)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, edgeURL+"/books", &body)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", loadM4PublicOrigin(t))
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	require.NoError(t, err)
	return response.StatusCode
}

func waitForM4Status(t *testing.T, environment m4Environment, edgeURL, bookID string, done func(m4Book) bool) m4Book {
	t.Helper()
	return waitForM4StatusObserving(t, environment, edgeURL, bookID, done)
}

func waitForM4StatusObserving(t *testing.T, environment m4Environment, edgeURL, bookID string, observe func(m4Book) bool) m4Book {
	t.Helper()
	deadline := time.Now().Add(environment.timeout)
	var last m4Book
	for time.Now().Before(deadline) {
		last = getM4Book(t, edgeURL, environment.accessToken, bookID)
		if observe(last) {
			return last
		}
		if last.ProcessingStatus == "failed" || last.ProcessingStage == "chunks_ready" {
			t.Fatalf("book reached unexpected terminal M4 projection: status=%q stage=%q category=%q", last.ProcessingStatus, last.ProcessingStage, last.ProcessingFailureCategory)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("book did not reach expected M4 projection; last status=%q stage=%q category=%q version=%d", last.ProcessingStatus, last.ProcessingStage, last.ProcessingFailureCategory, last.ProcessingVersion)
	return m4Book{}
}

func requireM4ArtifactReader(t *testing.T) {
	t.Helper()
	loadM4ArtifactEnvironment(t)
}

func fetchM4Manifest(t *testing.T, bookID string) []byte {
	t.Helper()
	environment := loadM4ArtifactEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, environment.dsn)
	if err != nil {
		t.Fatal("M4 artifact database connection configuration is invalid")
	}
	defer pool.Close()
	var receipt m4ArtifactReceipt
	err = pool.QueryRow(ctx, `SELECT manifest_reference, manifest_sha256, manifest_byte_size
		FROM ingestion.jobs WHERE book_id=$1 AND state='completed'`, bookID).Scan(&receipt.reference, &receipt.sha256, &receipt.byteSize)
	if err != nil {
		t.Fatal("completed M4 artifact receipt was not available in Ingestion Postgres")
	}
	contents := fetchM4Artifact(t, environment, receipt.reference)
	actualSHA := sha256.Sum256(contents)
	assert.True(t, bytes.Equal(receipt.sha256, actualSHA[:]), "manifest receipt checksum mismatch")
	assert.Equal(t, receipt.byteSize, int64(len(contents)))
	return contents
}

func fetchM4Artifact(t *testing.T, environment m4ArtifactEnvironment, reference string) []byte {
	t.Helper()
	client := newM4MinIOClient(t, environment)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	object, err := client.GetObject(ctx, environment.bucket, reference, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal("M4 private artifact read failed")
	}
	defer object.Close()
	contents, err := io.ReadAll(io.LimitReader(object, 65<<20))
	require.NoError(t, err)
	require.NotEmpty(t, contents)
	return contents
}

func loadM4ArtifactEnvironment(t *testing.T) m4ArtifactEnvironment {
	t.Helper()
	dsnFile := requiredM4PathOrSkip(t, "M4_E2E_INGESTION_POSTGRES_DSN_FILE")
	accessKeyFile := requiredM4PathOrSkip(t, "M4_E2E_MINIO_ACCESS_KEY_FILE")
	secretKeyFile := requiredM4PathOrSkip(t, "M4_E2E_MINIO_SECRET_KEY_FILE")
	environment := m4ArtifactEnvironment{
		dsn:       readM4SecretFile(t, dsnFile, 4096),
		endpoint:  strings.TrimSpace(os.Getenv("M4_E2E_MINIO_ENDPOINT")),
		accessKey: readM4SecretFile(t, accessKeyFile, 1024),
		secretKey: readM4SecretFile(t, secretKeyFile, 1024),
		bucket:    strings.TrimSpace(os.Getenv("M4_E2E_MINIO_ARTIFACT_BUCKET")),
		caFile:    strings.TrimSpace(os.Getenv("M4_E2E_MINIO_CA_FILE")),
	}
	if environment.endpoint == "" {
		t.Skip("M4_E2E_MINIO_ENDPOINT is required for private artifact validation")
	}
	if environment.bucket == "" {
		t.Skip("M4_E2E_MINIO_ARTIFACT_BUCKET is required for private artifact validation")
	}
	if strings.Contains(environment.endpoint, "://") {
		parsed, err := url.Parse(environment.endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.User != nil {
			t.Fatal("M4_E2E_MINIO_ENDPOINT must be a MinIO host or HTTP(S) origin")
		}
		environment.secure = parsed.Scheme == "https"
		environment.endpoint = parsed.Host
	} else if strings.ContainsAny(environment.endpoint, "/@?#") {
		t.Fatal("M4_E2E_MINIO_ENDPOINT must be a MinIO host or HTTP(S) origin")
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("M4_E2E_MINIO_INSECURE")), "true") {
		environment.secure = false
	}
	return environment
}

func newM4MinIOClient(t *testing.T, environment m4ArtifactEnvironment) *minio.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if environment.caFile != "" {
		certificate, err := os.ReadFile(environment.caFile) // #nosec G304 -- explicit test-only CA path.
		if err != nil {
			t.Fatal("M4 MinIO CA file could not be read")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(certificate) {
			t.Fatal("M4 MinIO CA file contained no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	client, err := minio.New(environment.endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(environment.accessKey, environment.secretKey, ""),
		Secure:    environment.secure,
		Transport: transport,
	})
	if err != nil {
		t.Fatal("M4 MinIO client configuration is invalid")
	}
	return client
}

func requiredM4PathOrSkip(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Skipf("%s is required for private artifact validation", key)
	}
	if !filepath.IsAbs(value) {
		t.Fatalf("%s must be an absolute path", key)
	}
	return value
}

func loadM4RecoveryControlDir(t *testing.T) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("M4_E2E_RECOVERY_CONTROL_DIR"))
	if value == "" {
		t.Skip("M4_E2E_RECOVERY_CONTROL_DIR is required for the externally orchestrated worker recovery test")
	}
	if !filepath.IsAbs(value) {
		t.Fatal("M4_E2E_RECOVERY_CONTROL_DIR must be an absolute path")
	}
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatal("M4_E2E_RECOVERY_CONTROL_DIR must be a non-symlink mode-0700 directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		t.Fatal("M4_E2E_RECOVERY_CONTROL_DIR must be owned by the test process user")
	}
	return filepath.Clean(value)
}

func removeM4RecoveryMarker(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal("M4 recovery marker could not be inspected")
	}
	if info.IsDir() {
		t.Fatal("M4 recovery marker path unexpectedly contains a directory")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal("M4 stale recovery marker could not be removed")
	}
}

func writeM4RecoveryMarker(t *testing.T, controlDir, target string, contents []byte) {
	t.Helper()
	if _, err := os.Lstat(target); err == nil || !os.IsNotExist(err) {
		t.Fatal("M4 recovery marker already exists")
	}
	temporary, err := os.CreateTemp(controlDir, ".upload-accepted-*")
	if err != nil {
		t.Fatal("M4 recovery marker temporary file could not be created")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil || closeErr != nil {
		t.Fatal("M4 recovery marker could not be written atomically")
	}
	if err = os.Rename(temporaryPath, target); err != nil {
		t.Fatal("M4 recovery marker could not be published atomically")
	}
}

func waitForM4WorkerRestarted(t *testing.T, controlDir, marker string, timeout time.Duration) {
	t.Helper()
	controlInfo, err := os.Lstat(controlDir)
	if err != nil {
		t.Fatal("M4 recovery control directory became unavailable")
	}
	controlStat, ok := controlInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("M4 recovery control directory ownership could not be verified")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		file, openErr := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- marker is inside the validated owner-only control directory.
		if openErr != nil {
			if os.IsNotExist(openErr) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			t.Fatal("M4 worker-restarted marker could not be opened without following links")
		}
		info, inspectErr := file.Stat()
		if inspectErr != nil {
			_ = file.Close()
			t.Fatal("M4 worker-restarted marker could not be inspected")
		}
		stat, validStat := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !validStat || stat.Uid != controlStat.Uid {
			_ = file.Close()
			t.Fatal("M4 worker-restarted marker must be a regular non-symlink mode-0600 owner file")
		}
		contents, readErr := io.ReadAll(io.LimitReader(file, 1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(contents) != 0 {
			t.Fatal("M4 worker-restarted marker must be empty")
		}
		return
	}
	t.Fatal("M4 worker-restarted marker did not arrive before its deadline")
}

func isCanonicalM4BookID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
				return false
			}
		}
	}
	return true
}

func assertM4SingularDurableState(t *testing.T, bookID string) {
	t.Helper()
	environment := loadM4ArtifactEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, environment.dsn)
	if err != nil {
		t.Fatal("M4 recovery database connection configuration is invalid")
	}
	defer pool.Close()
	var inboxCount, completedInboxCount, jobCount, completedJobCount, artifactSetCount, committedArtifactSetCount int64
	err = pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM ingestion.inbox WHERE business_key=$1),
		(SELECT COUNT(*) FROM ingestion.inbox WHERE business_key=$1 AND completed_at IS NOT NULL),
		(SELECT COUNT(*) FROM ingestion.jobs WHERE book_id=$1),
		(SELECT COUNT(*) FROM ingestion.jobs WHERE book_id=$1 AND state='completed'),
		(SELECT COUNT(*) FROM ingestion.artifact_sets a JOIN ingestion.jobs j ON j.id=a.job_id WHERE j.book_id=$1),
		(SELECT COUNT(*) FROM ingestion.artifact_sets a JOIN ingestion.jobs j ON j.id=a.job_id WHERE j.book_id=$1 AND a.committed_at IS NOT NULL)`, bookID).
		Scan(&inboxCount, &completedInboxCount, &jobCount, &completedJobCount, &artifactSetCount, &committedArtifactSetCount)
	if err != nil {
		t.Fatal("M4 recovery durable state could not be inspected")
	}
	assert.Equal(t, int64(1), inboxCount, "recovery created duplicate inbox state")
	assert.Equal(t, int64(1), completedInboxCount, "recovery did not complete its singular inbox state")
	assert.Equal(t, int64(1), jobCount, "recovery created duplicate job state")
	assert.Equal(t, int64(1), completedJobCount, "recovery did not complete its singular job state")
	assert.Equal(t, int64(1), artifactSetCount, "recovery created duplicate artifact state")
	assert.Equal(t, int64(1), committedArtifactSetCount, "recovery did not commit its singular artifact state")
}

func readM4SecretFile(t *testing.T, path string, maximumBytes int64) string {
	t.Helper()
	file, err := os.Open(path) // #nosec G304 -- explicit test-only secret path.
	if err != nil {
		t.Fatal("M4 test credential file could not be opened")
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(contents) == 0 || int64(len(contents)) > maximumBytes {
		t.Fatal("M4 test credential file has invalid bounded content")
	}
	value := strings.TrimSpace(string(contents))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		t.Fatal("M4 test credential file must contain one non-empty line")
	}
	return value
}

func fetchM4AcceptedEnvelope(t *testing.T, bookID string) ([]byte, string) {
	t.Helper()
	environment := loadM4ArtifactEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, environment.dsn)
	if err != nil {
		t.Fatal("M4 replay database connection configuration is invalid")
	}
	defer pool.Close()
	var eventID string
	var payload []byte
	err = pool.QueryRow(ctx, `SELECT event_id, payload FROM ingestion.inbox WHERE business_key=$1`, bookID).Scan(&eventID, &payload)
	if err != nil || eventID == "" || len(payload) == 0 || len(payload) > 256<<10 {
		t.Fatal("bounded original M4 envelope was not available for replay")
	}
	return payload, eventID
}

func publishM4UploadedEnvelope(t *testing.T, eventID string, payload []byte) {
	t.Helper()
	uriFile := requiredM4PathOrSkip(t, "M4_E2E_RABBITMQ_URI_FILE")
	uri := readM4SecretFile(t, uriFile, 4096)
	connection, err := amqp091.DialConfig(uri, amqp091.Config{Dial: amqp091.DefaultDial(5 * time.Second)})
	if err != nil {
		t.Fatal("M4 replay broker connection failed")
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal("M4 replay broker channel failed")
	}
	defer channel.Close()
	if err = channel.Confirm(false); err != nil {
		t.Fatal("M4 replay broker confirms unavailable")
	}
	confirmations := channel.NotifyPublish(make(chan amqp091.Confirmation, 1))
	returns := channel.NotifyReturn(make(chan amqp091.Return, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = channel.PublishWithContext(ctx, "raglibrarian.events.v1", "catalog.book.uploaded.v1", true, false, amqp091.Publishing{
		ContentType:  "application/x-protobuf",
		DeliveryMode: amqp091.Persistent,
		MessageId:    eventID,
		Type:         "catalog.book.uploaded.v1",
		Timestamp:    time.Now().UTC(),
		Body:         payload,
	})
	if err != nil {
		t.Fatal("M4 replay publish failed")
	}
	for {
		select {
		case <-returns:
			t.Fatal("M4 replay envelope was unroutable")
		case confirmation, open := <-confirmations:
			if !open || !confirmation.Ack {
				t.Fatal("M4 replay publish was not confirmed")
			}
			return
		case <-ctx.Done():
			t.Fatal("M4 replay confirmation timed out")
		}
	}
}

func randomM4MessageID(t *testing.T, prefix string) string {
	t.Helper()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal("M4 test message ID generation failed")
	}
	return prefix + hex.EncodeToString(random)
}

func waitForM4DeadLetter(t *testing.T, eventID string, timeout time.Duration) amqp091.Delivery {
	t.Helper()
	uriFile := requiredM4PathOrSkip(t, "M4_E2E_RABBITMQ_URI_FILE")
	uri := readM4SecretFile(t, uriFile, 4096)
	connection, err := amqp091.DialConfig(uri, amqp091.Config{Dial: amqp091.DefaultDial(5 * time.Second)})
	if err != nil {
		t.Fatal("M4 dead-letter broker connection failed")
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal("M4 dead-letter broker channel failed")
	}
	defer channel.Close()
	if err = channel.Qos(256, 0, false); err != nil {
		t.Fatal("M4 dead-letter consumer bound could not be configured")
	}
	consumer := randomM4MessageID(t, "m4-dlq-")
	deliveries, err := channel.Consume("ingestion.book-uploaded.dlq.v1", consumer, false, false, false, false, nil)
	if err != nil {
		t.Fatal("M4 dead-letter queue could not be consumed")
	}
	defer func() { _ = channel.Cancel(consumer, false) }()
	unrelated := make([]amqp091.Delivery, 0)
	defer func() {
		for _, delivery := range unrelated {
			if err := delivery.Nack(false, true); err != nil {
				t.Error("M4 unrelated dead-letter message could not be requeued")
			}
		}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case delivery, open := <-deliveries:
			if !open {
				t.Fatal("M4 dead-letter consumer closed before the expected message arrived")
			}
			if delivery.MessageId != eventID {
				unrelated = append(unrelated, delivery)
				if len(unrelated) >= 256 {
					t.Fatal("M4 dead-letter queue contained too many unrelated messages")
				}
				continue
			}
			if err := delivery.Ack(false); err != nil {
				t.Fatal("M4 expected dead-letter message could not be acknowledged")
			}
			return delivery
		case <-deadline.C:
			t.Fatal("M4 expected dead-letter message did not arrive before its deadline")
		}
	}
}

func assertM4DeadLetterConfidentiality(t *testing.T, delivery amqp091.Delivery, original []byte) {
	t.Helper()
	if failure := m4DeadLetterConfidentialityFailure(delivery, original); failure != "" {
		t.Fatal(failure)
	}
}

func m4DeadLetterConfidentialityFailure(delivery amqp091.Delivery, original []byte) string {
	forbidden := [][]byte{
		[]byte("%pdf-"),
		[]byte("m4_canary_"),
		[]byte("authorization:"),
		[]byte("bearer "),
		[]byte("password="),
		[]byte("postgres://"),
		[]byte("amqp://"),
		[]byte("dsn="),
	}
	if m4ContainsForbiddenDiagnostic(delivery.Body, forbidden) || m4TableContainsForbiddenDiagnostic(delivery.Headers, forbidden) {
		return "M4 dead-letter message exposed raw PDF content, a canary, or sensitive diagnostics"
	}
	if !bytes.Equal(original, delivery.Body) {
		return "M4 dead-letter body did not match the bounded poison envelope"
	}
	return ""
}

func m4TableContainsForbiddenDiagnostic(table amqp091.Table, forbidden [][]byte) bool {
	for key, value := range table {
		if m4ContainsForbiddenDiagnostic([]byte(key), forbidden) || m4ValueContainsForbiddenDiagnostic(value, forbidden) {
			return true
		}
	}
	return false
}

func m4ValueContainsForbiddenDiagnostic(value any, forbidden [][]byte) bool {
	switch typed := value.(type) {
	case string:
		return m4ContainsForbiddenDiagnostic([]byte(typed), forbidden)
	case []byte:
		return m4ContainsForbiddenDiagnostic(typed, forbidden)
	case amqp091.Table:
		return m4TableContainsForbiddenDiagnostic(typed, forbidden)
	case []any:
		for _, item := range typed {
			if m4ValueContainsForbiddenDiagnostic(item, forbidden) {
				return true
			}
		}
	}
	return false
}

func m4ContainsForbiddenDiagnostic(value []byte, forbidden [][]byte) bool {
	lower := bytes.ToLower(value)
	for _, pattern := range forbidden {
		if bytes.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func decodeM4Manifest(t *testing.T, contents []byte, bookID string) *ingestionv1.ChunkManifestV1 {
	t.Helper()
	var manifest ingestionv1.ChunkManifestV1
	require.NoError(t, proto.Unmarshal(contents, &manifest))
	assert.Equal(t, "v1", manifest.SchemaVersion)
	assert.Equal(t, bookID, manifest.BookId)
	assert.Len(t, manifest.SourceSha256, sha256.Size)
	assert.Len(t, manifest.ProcessingConfigDigest, sha256.Size)
	assert.NotEmpty(t, manifest.ExtractionVersion)
	assert.NotEmpty(t, manifest.NormalizationVersion)
	assert.NotEmpty(t, manifest.TokenizerVersion)
	assert.NotEmpty(t, manifest.ChunkingVersion)
	assert.NotEmpty(t, manifest.StructureVersion)
	assert.Equal(t, uint32(800), manifest.MaximumTokens)
	assert.Equal(t, uint32(120), manifest.OverlapTokens)
	assert.Positive(t, manifest.PageCount)
	assert.Positive(t, manifest.ChunkCount)
	require.NotNil(t, manifest.GeneratedAt)
	assert.NoError(t, manifest.GeneratedAt.CheckValid())
	require.NotEmpty(t, manifest.Shards)
	return &manifest
}

func fetchM4Chunks(t *testing.T, manifest *ingestionv1.ChunkManifestV1) []*ingestionv1.ChunkV1 {
	t.Helper()
	environment := loadM4ArtifactEnvironment(t)
	chunks := make([]*ingestionv1.ChunkV1, 0, manifest.ChunkCount)
	var nextOrder uint64
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(128<<20))
	require.NoError(t, err)
	defer decoder.Close()
	for _, descriptor := range manifest.Shards {
		require.NotNil(t, descriptor)
		require.NotEmpty(t, descriptor.Reference)
		require.Len(t, descriptor.Sha256, sha256.Size)
		compressed := fetchM4Artifact(t, environment, descriptor.Reference)
		actualSHA := sha256.Sum256(compressed)
		assert.True(t, bytes.Equal(descriptor.Sha256, actualSHA[:]), "shard checksum mismatch")
		assert.Equal(t, descriptor.CompressedByteSize, int64(len(compressed)))
		uncompressed, err := decoder.DecodeAll(compressed, nil)
		require.NoError(t, err)
		assert.Equal(t, descriptor.UncompressedByteSize, int64(len(uncompressed)))
		shardChunks := decodeM4ChunkRecords(t, uncompressed)
		require.Len(t, shardChunks, int(descriptor.ChunkCount))
		require.NotEmpty(t, shardChunks)
		assert.Equal(t, nextOrder, descriptor.FirstChunkOrder)
		assert.Equal(t, shardChunks[0].Order, descriptor.FirstChunkOrder)
		assert.Equal(t, shardChunks[len(shardChunks)-1].Order, descriptor.LastChunkOrder)
		nextOrder = descriptor.LastChunkOrder + 1
		chunks = append(chunks, shardChunks...)
	}
	return chunks
}

func decodeM4ChunkRecords(t *testing.T, contents []byte) []*ingestionv1.ChunkV1 {
	t.Helper()
	var chunks []*ingestionv1.ChunkV1
	for len(contents) > 0 {
		size, consumed := protowire.ConsumeVarint(contents)
		if consumed < 0 || size > uint64(len(contents)-consumed) {
			t.Fatal("invalid length-delimited chunk record")
		}
		contents = contents[consumed:]
		chunk := new(ingestionv1.ChunkV1)
		require.NoError(t, proto.Unmarshal(contents[:size], chunk))
		chunks = append(chunks, chunk)
		contents = contents[size:]
	}
	return chunks
}

func assertCrossPageStructure(t *testing.T, chunks []*ingestionv1.ChunkV1) {
	t.Helper()
	carried := false
	for _, chunk := range chunks {
		if chunk.PageStart <= 2 && chunk.PageEnd >= 2 && chunk.Chapter != "" {
			carried = true
			break
		}
	}
	assert.True(t, carried, "chapter context was not carried onto the unheaded second page")
}

func getM4Book(t *testing.T, edgeURL, token, bookID string) m4Book {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, edgeURL+"/books/"+url.PathEscape(bookID), nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	var book m4Book
	decodeJSON(t, response, &book)
	return book
}

func openM4EventStream(t *testing.T, ctx context.Context, edgeURL, publicOrigin, token, lastEventID string) *m4EventStream {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, edgeURL+"/books/events", nil)
	require.NoError(t, err)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Origin", publicOrigin)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	cacheControl := strings.ToLower(response.Header.Get("Cache-Control"))
	require.Contains(t, cacheControl, "no-store")
	require.Contains(t, cacheControl, "private")
	t.Cleanup(func() { _ = response.Body.Close() })
	return &m4EventStream{body: response.Body, reader: bufio.NewReaderSize(response.Body, 64<<10)}
}

func loadM4PublicOrigin(t *testing.T) string {
	t.Helper()
	origin := strings.TrimRight(strings.TrimSpace(os.Getenv("M4_E2E_PUBLIC_ORIGIN")), "/")
	if origin == "" {
		t.Fatal("M4_E2E_PUBLIC_ORIGIN is required")
	}
	return origin
}

func readM4SSEEvent(stream *m4EventStream) (m4SSEEvent, error) {
	var event m4SSEEvent
	for {
		rawLine, err := stream.reader.ReadSlice('\n')
		if err != nil && len(rawLine) == 0 {
			return m4SSEEvent{}, err
		}
		if err == bufio.ErrBufferFull {
			return m4SSEEvent{}, fmt.Errorf("SSE line exceeds 64 KiB")
		}
		line := strings.TrimSuffix(strings.TrimSuffix(string(rawLine), "\n"), "\r")
		if line == "" {
			if event.eventType != "" || len(event.data) > 0 {
				return event, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "id":
			event.id = value
		case "event":
			event.eventType = value
		case "data":
			if len(event.data) > 0 {
				event.data = append(event.data, '\n')
			}
			event.data = append(event.data, value...)
		}
		if err == io.EOF {
			if event.eventType != "" || len(event.data) > 0 {
				return event, nil
			}
			return m4SSEEvent{}, io.EOF
		}
	}
}

func waitForM4ReadyEvent(t *testing.T, stream *m4EventStream, bookID string) (m4SSEPayload, string) {
	t.Helper()
	for {
		event, err := readM4SSEEvent(stream)
		require.NoError(t, err)
		if event.eventType == "books-resync" {
			continue
		}
		require.Equal(t, "book-processing-status-changed", event.eventType)
		require.NotEmpty(t, event.id)
		require.LessOrEqual(t, len(event.id), 128)
		require.NotContains(t, event.id, "\r")
		require.NotContains(t, event.id, "\n")
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(event.data, &raw))
		allowed := map[string]bool{
			"book_id": true, "processing_status": true, "processing_stage": true,
			"processing_failure_category": true, "processing_version": true,
			"updated_at": true, "schema_version": true,
		}
		for key := range raw {
			assert.True(t, allowed[key], "SSE payload exposed unexpected field %q", key)
		}
		for _, key := range []string{"book_id", "processing_status", "processing_stage", "processing_version", "updated_at", "schema_version"} {
			assert.Contains(t, raw, key)
		}
		var payload m4SSEPayload
		require.NoError(t, json.Unmarshal(event.data, &payload))
		if bytes.Contains(event.data, []byte("Synthetic chapter")) || bytes.Contains(event.data, []byte("M4_CANARY_")) {
			t.Fatal("SSE payload exposed synthetic document content")
		}
		if payload.BookID == bookID && payload.ProcessingStage == "chunks_ready" {
			propagationDelay := time.Since(payload.UpdatedAt)
			assert.GreaterOrEqual(t, propagationDelay, -m4ReadyPropagationSLO, "clock skew exceeds the SLO tolerance")
			assert.LessOrEqual(t, propagationDelay, m4ReadyPropagationSLO, "%s ready propagation exceeded", m4SLOProfile)
			return payload, event.id
		}
	}
}

func waitForM4SLOEvents(t *testing.T, stream *m4EventStream, bookID string, startedAt time.Time) {
	t.Helper()
	extractingObserved := false
	for {
		event, err := readM4SSEEvent(stream)
		require.NoError(t, err)
		if event.eventType == "books-resync" {
			continue
		}
		require.Equal(t, "book-processing-status-changed", event.eventType)
		var payload m4SSEPayload
		require.NoError(t, json.Unmarshal(event.data, &payload))
		if payload.BookID != bookID {
			continue
		}
		switch payload.ProcessingStage {
		case "extracting":
			extractingObserved = true
			assert.LessOrEqual(t, time.Since(startedAt), m4ExtractingVisibilitySLO, "%s extracting visibility exceeded", m4SLOProfile)
		case "chunks_ready":
			assert.True(t, extractingObserved, "%s requires an externally visible extracting stage", m4SLOProfile)
			propagationDelay := time.Since(payload.UpdatedAt)
			assert.GreaterOrEqual(t, propagationDelay, -m4ReadyPropagationSLO, "clock skew exceeds the SLO tolerance")
			assert.LessOrEqual(t, propagationDelay, m4ReadyPropagationSLO, "%s ready propagation exceeded", m4SLOProfile)
			return
		case "failed":
			t.Fatalf("%s document failed with category %q", m4SLOProfile, payload.ProcessingFailureCategory)
		}
	}
}
