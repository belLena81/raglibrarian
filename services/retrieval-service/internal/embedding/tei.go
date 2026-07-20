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

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

const maximumResponseBytes = 8 << 20

type TEI struct {
	endpoint string
	client   *http.Client
}

func NewTEI(endpoint string, client *http.Client) (*TEI, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || client == nil {
		return nil, errors.New("invalid TEI configuration")
	}
	return &TEI{endpoint: strings.TrimRight(endpoint, "/"), client: client}, nil
}

func (t *TEI) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := t.embed(ctx, text, 1)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (t *TEI) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > 256 {
		return nil, errors.New("invalid embedding batch")
	}
	const providerBatchSize = 32
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += providerBatchSize {
		end := start + providerBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vectors, err := t.embed(ctx, texts[start:end], end-start)
		if err != nil {
			return nil, err
		}
		result = append(result, vectors...)
	}
	return result, nil
}

func (t *TEI) embed(ctx context.Context, inputs any, expected int) ([][]float32, error) {
	body, err := json.Marshal(struct {
		Inputs   any  `json:"inputs"`
		Truncate bool `json:"truncate"`
	}{Inputs: inputs, Truncate: false})
	if err != nil {
		return nil, errors.New("encode embedding request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create embedding request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(request) // #nosec G704 -- NewTEI accepts only a validated operator-controlled private endpoint.
	if err != nil {
		return nil, errors.New("embedding dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseBytes))
		return nil, fmt.Errorf("embedding dependency status %d", response.StatusCode)
	}
	var vectors [][]float32
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBytes))
	if err = decoder.Decode(&vectors); err != nil || len(vectors) != expected {
		return nil, errors.New("invalid embedding response")
	}
	for _, vector := range vectors {
		if len(vector) != domain.EmbeddingDimensions {
			return nil, errors.New("invalid embedding response")
		}
	}
	return vectors, nil
}

func (t *TEI) CheckReady(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint+"/health", nil)
	if err != nil {
		return errors.New("create embedding readiness request")
	}
	response, err := t.client.Do(request) // #nosec G704 -- NewTEI accepts only a validated operator-controlled private endpoint.
	if err != nil {
		return errors.New("embedding dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseBytes))
	if response.StatusCode != http.StatusOK {
		return errors.New("embedding dependency unavailable")
	}
	return nil
}
