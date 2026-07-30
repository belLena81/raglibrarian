package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeRPC struct {
	request *retrievalv1.SearchRequest
	ids     []string
	check   *retrievalv1.CheckResponse
	err     error
}

func (f *fakeRPC) Check(context.Context, *retrievalv1.CheckRequest, ...grpc.CallOption) (*retrievalv1.CheckResponse, error) {
	if f.check == nil {
		f.check = &retrievalv1.CheckResponse{Status: "SERVING"}
	}
	return f.check, f.err
}
func (f *fakeRPC) Search(ctx context.Context, request *retrievalv1.SearchRequest, _ ...grpc.CallOption) (*retrievalv1.SearchResponse, error) {
	f.request = request
	metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
	f.ids = metadata.Get("x-request-id")
	if f.err != nil {
		return nil, f.err
	}
	return &retrievalv1.SearchResponse{
		Query: request.Question,
		QueryMatch: &retrievalv1.QueryMatchMetadata{
			QueryEmbedding:   []float32{1, 0},
			EmbeddingProfile: "embedding-profile",
			RetrievalProfile: "retrieval-profile",
			CorpusSnapshot:   "snapshot",
		},
		Results: []*retrievalv1.Evidence{{
			EvidenceId: "e-1", Passage: "passage", Summary: "passage summary",
			Book: &retrievalv1.BookMetadata{BookId: "book-1", Title: "Book", Author: "Author", Year: 2024, MediaType: "application/epub+zip"},
		}},
		Documents: []*retrievalv1.DocumentResult{{
			DocumentId: "doc-1",
			ChunkCount: 1,
			PageStart:  1,
			PageEnd:    2,
			Score:      0.9,
			Summary:    "document summary",
			Evidence: []*retrievalv1.Evidence{{
				EvidenceId: "e-1", Passage: "passage", Summary: "passage summary",
				Book: &retrievalv1.BookMetadata{BookId: "book-1", Title: "Book", Author: "Author", Year: 2024, MediaType: "application/epub+zip"},
			}},
			Book: &retrievalv1.BookMetadata{BookId: "book-1", Title: "Book", Author: "Author", Year: 2024, MediaType: "application/epub+zip"},
		}},
	}, nil
}

func TestClientForwardsActorCorrelationAndMapsEvidence(t *testing.T) {
	rpc := &fakeRPC{}
	client := NewClient(rpc)
	id := strings.Repeat("a", 32)
	result, err := client.Search(context.Background(), domain.SearchRequest{Question: "question", Limit: 5,
		Actor: domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: id, IncludeQueryMatchMetadata: true})
	if err != nil || rpc.request.Actor.UserId != "user-1" || rpc.request.CorrelationId != id || !rpc.request.IncludeQueryMatchMetadata || len(rpc.ids) != 1 || rpc.ids[0] != id || result.Results[0].EvidenceID != "e-1" ||
		result.Results[0].Summary != "passage summary" || result.Results[0].Book.MediaType != "application/epub+zip" || len(result.Documents) != 1 || result.Documents[0].Summary != "document summary" ||
		result.Documents[0].Book.MediaType != "application/epub+zip" || len(result.Documents[0].Evidence) != 1 || result.Documents[0].Evidence[0].Summary != "passage summary" ||
		result.Documents[0].Evidence[0].Book.MediaType != "application/epub+zip" || len(result.QueryEmbedding) != 2 ||
		result.EmbeddingProfile != "embedding-profile" || result.RetrievalProfile != "retrieval-profile" || result.CorpusSnapshot != "snapshot" {
		t.Fatalf("Search() = %#v, %v; request=%#v ids=%#v", result, err, rpc.request, rpc.ids)
	}
}

func TestClientOmitsQueryMatchMetadataWhenNotRequested(t *testing.T) {
	rpc := &fakeRPC{}
	client := NewClient(rpc)
	if _, err := client.Search(context.Background(), domain.SearchRequest{
		Question:      "question",
		CorrelationID: strings.Repeat("a", 32),
	}); err != nil {
		t.Fatal(err)
	}
	if rpc.request.IncludeQueryMatchMetadata {
		t.Fatal("query-match metadata requested for disabled cache")
	}
}

func TestClientPreservesCancellationAndDeadlineErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "canceled", err: status.Error(codes.Canceled, "remote detail"), want: context.Canceled},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "remote detail"), want: context.DeadlineExceeded},
		{name: "unavailable", err: status.Error(codes.Unavailable, "remote detail"), want: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(&fakeRPC{err: test.err})
			if _, err := client.Search(context.Background(), domain.SearchRequest{}); !errors.Is(err, test.want) {
				t.Fatalf("Search() error = %v, want %v", err, test.want)
			}
			if err := client.CheckReady(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("CheckReady() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestClientPrefersLocalContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(&fakeRPC{err: status.Error(codes.Unavailable, "remote detail")})

	if _, err := client.Search(ctx, domain.SearchRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context canceled", err)
	}
}
