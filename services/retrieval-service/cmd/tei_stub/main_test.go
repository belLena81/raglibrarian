package main

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestEmbedReturnsDeterministicNormalizedVectors(t *testing.T) {
	body := bytes.NewBufferString(`{"inputs":["Deterministic output makes retries harmless","deterministic retries"],"truncate":false}`)
	request := httptest.NewRequest(http.MethodPost, "/embed", body)
	response := httptest.NewRecorder()

	embed(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("embed status = %d, want 200", response.Code)
	}
	var vectors [][]float32
	if err := json.Unmarshal(response.Body.Bytes(), &vectors); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != domain.EmbeddingDimensions || len(vectors[1]) != domain.EmbeddingDimensions {
		t.Fatalf("unexpected vector shape: %d x %d", len(vectors), len(vectors[0]))
	}
	if cosine(vectors[0], vectors[1]) <= 0 {
		t.Fatal("related texts did not share deterministic vector dimensions")
	}
	if math.Abs(float64(cosine(vectors[0], vectors[0])-1)) > 0.0001 {
		t.Fatal("document vector is not normalized")
	}
}

func TestEmbedRejectsEmptyInput(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/embed", bytes.NewBufferString(`{"inputs":[""]}`))
	response := httptest.NewRecorder()

	embed(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("embed status = %d, want 400", response.Code)
	}
}

func cosine(left, right []float32) float32 {
	var score float32
	for index := range left {
		score += left[index] * right[index]
	}
	return score
}
