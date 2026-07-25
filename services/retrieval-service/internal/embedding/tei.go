// Package embedding implements the private TEI outbound adapter.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/throttle"
	"go.uber.org/zap"
)

const maximumResponseBytes = 8 << 20
const providerBatchSize = 8

type TEI struct {
	endpoint string
	client   *http.Client
	log      *zap.Logger
	limit    *throttle.Limiter
}

func NewTEI(endpoint string, client *http.Client, log *zap.Logger, limit *throttle.Limiter) (*TEI, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || client == nil {
		return nil, errors.New("invalid TEI configuration")
	}
	return &TEI{endpoint: strings.TrimRight(endpoint, "/"), client: client, log: log, limit: limit}, nil
}

func (t *TEI) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := t.embed(ctx, text, 1, "embed_query")
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (t *TEI) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > 256 {
		return nil, errors.New("invalid embedding batch")
	}
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += providerBatchSize {
		end := start + providerBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vectors, err := t.embed(ctx, texts[start:end], end-start, "embed_documents")
		if err != nil {
			return nil, err
		}
		result = append(result, vectors...)
	}
	return result, nil
}

func (t *TEI) embed(ctx context.Context, inputs any, expected int, operation string) ([][]float32, error) {
	started := time.Now()
	if wait, err := t.wait(ctx); err != nil {
		return nil, t.failure(operation, "provider_rate_limited", "throttle", sanitizeEmbeddingDetail(wait.String()), 0, 0, started, err)
	} else if wait > 0 && t.log != nil {
		t.log.Info("retrieval.embedding.request.throttled",
			zap.String("operation", operation),
			zap.Int64("wait_ms", wait.Milliseconds()),
		)
	}
	body, err := json.Marshal(struct {
		Inputs   any  `json:"inputs"`
		Truncate bool `json:"truncate"`
	}{Inputs: inputs, Truncate: false})
	if err != nil {
		return nil, t.failure(operation, "request_encode_failed", "request", "", 0, 0, started, errors.New("encode embedding request"))
	}
	requestBytes := len(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, t.failure(operation, "request_create_failed", "request", "", 0, requestBytes, started, errors.New("create embedding request"))
	}
	request.Header.Set("Content-Type", "application/json")
	if t.log != nil {
		t.log.Info("retrieval.embedding.request.started",
			zap.String("operation", operation),
			zap.Int("input_count", expected),
			zap.Int("request_bytes", requestBytes),
		)
	}
	response, err := t.client.Do(request) // #nosec G704 -- NewTEI accepts only a validated operator-controlled private endpoint.
	if err != nil {
		return nil, t.failure(operation, classifyEmbeddingRequestError(err), "request", sanitizeEmbeddingDetail(err.Error()), 0, requestBytes, started, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes))
		detail := sanitizeEmbeddingDetail(string(body))
		if detail == "" {
			detail = sanitizeEmbeddingDetail(response.Status)
		}
		return nil, t.failure(operation, fmt.Sprintf("provider_http_status_%d", response.StatusCode), "response", detail, response.StatusCode, requestBytes, started, fmt.Errorf("embedding dependency status %d", response.StatusCode))
	}
	var vectors [][]float32
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBytes))
	if err = decoder.Decode(&vectors); err != nil || len(vectors) != expected {
		return nil, t.failure(operation, "invalid_embedding_response", "validation", "", response.StatusCode, requestBytes, started, errors.New("invalid embedding response"))
	}
	for _, vector := range vectors {
		if len(vector) != domain.EmbeddingDimensions {
			return nil, t.failure(operation, "invalid_embedding_response", "validation", "", response.StatusCode, requestBytes, started, errors.New("invalid embedding response"))
		}
	}
	if t.log != nil {
		t.log.Info("retrieval.embedding.request.completed",
			zap.String("operation", operation),
			zap.Int("input_count", expected),
			zap.Int("response_vectors", len(vectors)),
			zap.Int64("duration_ms", time.Since(started).Milliseconds()),
			zap.Int("status_code", response.StatusCode),
		)
	}
	return vectors, nil
}

func (t *TEI) CheckReady(ctx context.Context) error {
	readinessPaths := []string{"/readyz", "/healthz", "/health"}
	for _, path := range readinessPaths {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint+path, nil)
		if err != nil {
			return errors.New("create embedding readiness request")
		}
		response, err := t.client.Do(request) // #nosec G704 -- NewTEI accepts only a validated operator-controlled private endpoint.
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseBytes))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK {
			return nil
		}
	}
	return errors.New("embedding dependency unavailable")
}

func (t *TEI) failure(operation, code, stage, detail string, statusCode, requestBytes int, started time.Time, err error) error {
	if t.log != nil {
		fields := []zap.Field{
			zap.String("operation", operation),
			zap.String("stage", stage),
			zap.String("reason_code", code),
			zap.Int64("duration_ms", time.Since(started).Milliseconds()),
		}
		if detail != "" {
			fields = append(fields, zap.String("reason_detail", detail))
		}
		if statusCode > 0 {
			fields = append(fields, zap.Int("status_code", statusCode))
		}
		if requestBytes > 0 {
			fields = append(fields, zap.Int("request_bytes", requestBytes))
		}
		t.log.Warn("retrieval.embedding.request.failed", fields...)
	}
	return err
}

func (t *TEI) wait(ctx context.Context) (time.Duration, error) {
	if t.limit == nil {
		return 0, nil
	}
	return t.limit.Wait(ctx)
}

func sanitizeEmbeddingDetail(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return ""
	}
	if len([]rune(value)) > 160 {
		value = string([]rune(value)[:160])
	}
	return value
}

func classifyEmbeddingRequestError(err error) string {
	if err == nil {
		return "provider_unavailable"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "provider_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "provider_canceled"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return classifyEmbeddingRequestError(urlErr.Err)
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "provider_timeout"
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "x509:"):
		return "provider_tls_error"
	case strings.Contains(text, "certificate"):
		return "provider_tls_error"
	case strings.Contains(text, "no such host"), strings.Contains(text, "lookup "):
		return "provider_dns_error"
	case strings.Contains(text, "connection refused"), strings.Contains(text, "connect: connection timed out"), strings.Contains(text, "network is unreachable"), strings.Contains(text, "connection reset by peer"):
		return "provider_network_error"
	default:
		return "provider_transport_error"
	}
}
