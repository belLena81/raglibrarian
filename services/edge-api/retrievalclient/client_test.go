package retrievalclient

import (
	"context"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

const testRequestID = "0123456789abcdef0123456789abcdef"

type retrievalClientStub struct {
	retrievalv1.RetrievalServiceClient
	search func(context.Context, *retrievalv1.SearchRequest, ...grpc.CallOption) (*retrievalv1.SearchResponse, error)
}

func (s *retrievalClientStub) Search(ctx context.Context, request *retrievalv1.SearchRequest, options ...grpc.CallOption) (*retrievalv1.SearchResponse, error) {
	return s.search(ctx, request, options...)
}

func TestSearchMapsRequestResponseMetadataAndDeadline(t *testing.T) {
	yearFrom := 2020
	yearTo := 2025
	stub := &retrievalClientStub{}
	stub.search = func(ctx context.Context, request *retrievalv1.SearchRequest, _ ...grpc.CallOption) (*retrievalv1.SearchResponse, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 3*time.Second)
		metadata, ok := grpcmetadata.FromOutgoingContext(ctx)
		require.True(t, ok)
		assert.Equal(t, []string{testRequestID}, metadata.Get("x-request-id"))
		assert.Equal(t, testRequestID, request.CorrelationId)
		assert.Equal(t, "trusted-user", request.Actor.UserId)
		assert.Equal(t, "reader", request.Actor.Role)
		assert.Equal(t, "active", request.Actor.Status)
		assert.Equal(t, int32(2020), request.Filters.GetYearFrom())
		assert.Equal(t, int32(2025), request.Filters.GetYearTo())
		return &retrievalv1.SearchResponse{
			Query: request.Question,
			Results: []*retrievalv1.Evidence{{
				EvidenceId: "evidence-1", ChunkId: "chunk-1",
				Book:    &retrievalv1.BookMetadata{BookId: "book-1", Title: "Book", Author: "Author", Year: 2024, Tags: []string{"systems"}},
				Chapter: "Replication", Section: "Quorums", PageStart: 5, PageEnd: 6,
				Passage: "stored passage", Score: 0.9,
			}},
			Documents: []*retrievalv1.DocumentResult{{
				DocumentId: "book-1:job-1",
				Book:       &retrievalv1.BookMetadata{BookId: "book-1", Title: "Book", Author: "Author", Year: 2024, Tags: []string{"systems"}},
				ChunkCount: 10, PageStart: 1, PageEnd: 100, Score: 0.8,
				Evidence: []*retrievalv1.Evidence{{
					EvidenceId: "evidence-1", ChunkId: "chunk-1",
					Book:    &retrievalv1.BookMetadata{BookId: "book-1", Title: "Book", Author: "Author", Year: 2024, Tags: []string{"systems"}},
					Chapter: "Replication", Section: "Quorums", PageStart: 5, PageEnd: 6,
					Passage: "stored passage", Score: 0.9,
				}},
			}},
		}, nil
	}
	client := New(stub)
	ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, testRequestID)

	result, err := client.Search(ctx, handler.SearchRequest{
		Question: "How?", Filters: handler.SearchFilters{Tags: []string{"systems"}, Author: "Author", YearFrom: &yearFrom, YearTo: &yearTo}, Limit: 7,
		Actor: handler.SearchActor{UserID: "trusted-user", Role: "reader", Status: "active"},
	})

	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	assert.Equal(t, "evidence-1", result.Results[0].EvidenceID)
	assert.Equal(t, "book-1", result.Results[0].Book.ID)
	assert.Equal(t, uint32(5), result.Results[0].PageStart)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "book-1:job-1", result.Documents[0].DocumentID)
	assert.Equal(t, uint32(10), result.Documents[0].ChunkCount)
	require.Len(t, result.Documents[0].Evidence, 1)
}

func TestSearchMapsStableRetrievalFailures(t *testing.T) {
	tests := []struct {
		code codes.Code
		want error
	}{
		{code: codes.InvalidArgument, want: handler.ErrInvalidSearch},
		{code: codes.PermissionDenied, want: handler.ErrSearchForbidden},
		{code: codes.Unauthenticated, want: handler.ErrSearchForbidden},
		{code: codes.Unavailable, want: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.code.String(), func(t *testing.T) {
			client := New(&retrievalClientStub{search: func(context.Context, *retrievalv1.SearchRequest, ...grpc.CallOption) (*retrievalv1.SearchResponse, error) {
				return nil, status.Error(test.code, "private retrieval detail")
			}})
			ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, testRequestID)

			_, err := client.Search(ctx, handler.SearchRequest{Question: "q"})

			assert.ErrorIs(t, err, test.want)
			assert.NotContains(t, err.Error(), "private retrieval detail")
		})
	}
}

func TestSearchRejectsInvalidRequestIDBeforeRPC(t *testing.T) {
	called := false
	client := New(&retrievalClientStub{search: func(context.Context, *retrievalv1.SearchRequest, ...grpc.CallOption) (*retrievalv1.SearchResponse, error) {
		called = true
		return &retrievalv1.SearchResponse{}, nil
	}})

	_, err := client.Search(context.Background(), handler.SearchRequest{Question: "q"})

	assert.ErrorIs(t, err, ErrUnavailable)
	assert.False(t, called)
}
