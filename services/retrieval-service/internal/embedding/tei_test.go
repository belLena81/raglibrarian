package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestTEIEmbedsWithoutTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/embed" || request.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			Inputs   string `json:"inputs"`
			Truncate bool   `json:"truncate"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Inputs != "replication" || body.Truncate {
			t.Fatalf("unexpected body: %#v", body)
		}
		vector := make([]float32, domain.EmbeddingDimensions)
		_ = json.NewEncoder(writer).Encode([][]float32{vector})
	}))
	defer server.Close()

	client, err := NewTEI(server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewTEI() error = %v", err)
	}
	vector, err := client.EmbedQuery(context.Background(), "replication")
	if err != nil || len(vector) != domain.EmbeddingDimensions {
		t.Fatalf("EmbedQuery() length = %d, error = %v", len(vector), err)
	}
}
