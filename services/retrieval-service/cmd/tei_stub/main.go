package main

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

const maximumRequestBytes = 1 << 20

func main() {
	address := os.Getenv("TEI_STUB_ADDR")
	if address == "" {
		address = ":8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("POST /embed", embed)
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func embed(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var request struct {
		Inputs   json.RawMessage `json:"inputs"`
		Truncate bool            `json:"truncate"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maximumRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || len(request.Inputs) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	inputs, err := decodeInputs(request.Inputs)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	vectors := make([][]float32, 0, len(inputs))
	for _, input := range inputs {
		vectors = append(vectors, vectorForText(input))
	}
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(vectors); err != nil {
		log.Print("encode embedding response")
	}
}

func decodeInputs(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, errors.New("empty input")
		}
		return []string{single}, nil
	}
	var batch []string
	if err := json.Unmarshal(raw, &batch); err != nil || len(batch) == 0 || len(batch) > 256 {
		return nil, errors.New("invalid input")
	}
	for _, input := range batch {
		if strings.TrimSpace(input) == "" {
			return nil, errors.New("empty input")
		}
	}
	return batch, nil
}

func vectorForText(text string) []float32 {
	vector := make([]float32, domain.EmbeddingDimensions)
	for _, token := range textTokens(text) {
		index := stableIndex(token)
		vector[index]++
	}
	var magnitude float64
	for _, value := range vector {
		magnitude += float64(value * value)
	}
	if magnitude == 0 {
		vector[0] = 1
		return vector
	}
	scale := float32(1 / math.Sqrt(magnitude))
	for index, value := range vector {
		vector[index] = value * scale
	}
	return vector
}

func textTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(value rune) bool {
		return !unicode.IsLetter(value) && !unicode.IsNumber(value)
	})
}

func stableIndex(token string) int {
	const embeddingDimensions = uint32(domain.EmbeddingDimensions)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(token))
	return int(hash.Sum32() % embeddingDimensions)
}
