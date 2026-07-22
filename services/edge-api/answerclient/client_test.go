package answerclient

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

	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

const testRequestID = "0123456789abcdef0123456789abcdef"

type answerClientStub struct {
	answerv1.AnswerServiceClient
	answer func(context.Context, *answerv1.AnswerRequest, ...grpc.CallOption) (*answerv1.AnswerResponse, error)
}

func (s *answerClientStub) Answer(ctx context.Context, request *answerv1.AnswerRequest, options ...grpc.CallOption) (*answerv1.AnswerResponse, error) {
	return s.answer(ctx, request, options...)
}

func TestAnswerPropagatesRequestIDDeadlineActorAndMapsResponse(t *testing.T) {
	stub := &answerClientStub{}
	stub.answer = func(ctx context.Context, request *answerv1.AnswerRequest, _ ...grpc.CallOption) (*answerv1.AnswerResponse, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 8*time.Second)
		metadata, ok := grpcmetadata.FromOutgoingContext(ctx)
		require.True(t, ok)
		assert.Equal(t, []string{testRequestID}, metadata.Get("x-request-id"))
		assert.Equal(t, testRequestID, request.Search.CorrelationId)
		assert.Equal(t, "trusted-user", request.Search.Actor.UserId)
		assert.Equal(t, "reader", request.Search.Actor.Role)
		assert.Equal(t, "active", request.Search.Actor.Status)
		return &answerv1.AnswerResponse{
			Search: &retrievalv1.SearchResponse{
				Query:   request.Search.Question,
				Results: []*retrievalv1.Evidence{{EvidenceId: "evidence-1", Passage: "stored passage"}},
			},
			Answer: &answerv1.GroundedAnswer{Segments: []*answerv1.AnswerSegment{{
				Text: "Grounded.", EvidenceIds: []string{"evidence-1"},
			}}},
		}, nil
	}
	client := New(stub, 8*time.Second)
	ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, testRequestID)

	result, err := client.Answer(ctx, handler.SearchRequest{
		Question: "How?", Limit: 5,
		Actor: handler.SearchActor{UserID: "trusted-user", Role: "reader", Status: "active"},
	})

	require.NoError(t, err)
	require.Len(t, result.Search.Results, 1)
	assert.Equal(t, "evidence-1", result.Search.Results[0].EvidenceID)
	require.NotNil(t, result.Answer)
	require.Len(t, result.Answer.Segments, 1)
	assert.Equal(t, "Grounded.", result.Answer.Segments[0].Text)
	assert.Equal(t, []string{"evidence-1"}, result.Answer.Segments[0].EvidenceIDs)
}

func TestAnswerMapsFallbackAndStableFailures(t *testing.T) {
	tests := []struct {
		code codes.Code
		want error
	}{
		{code: codes.InvalidArgument, want: handler.ErrInvalidSearch},
		{code: codes.PermissionDenied, want: handler.ErrSearchForbidden},
		{code: codes.Unauthenticated, want: handler.ErrSearchForbidden},
		{code: codes.Unavailable, want: handler.ErrAnswerUnavailable},
		{code: codes.DeadlineExceeded, want: handler.ErrAnswerUnavailable},
		{code: codes.ResourceExhausted, want: handler.ErrAnswerUnavailable},
		{code: codes.Internal, want: handler.ErrAnswerFailed},
	}
	for _, test := range tests {
		t.Run(test.code.String(), func(t *testing.T) {
			client := New(&answerClientStub{answer: func(context.Context, *answerv1.AnswerRequest, ...grpc.CallOption) (*answerv1.AnswerResponse, error) {
				return nil, status.Error(test.code, "private answer detail")
			}}, time.Second)
			ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, testRequestID)

			_, err := client.Answer(ctx, handler.SearchRequest{Question: "private question"})

			assert.ErrorIs(t, err, test.want)
			assert.NotContains(t, err.Error(), "private answer detail")
			assert.NotContains(t, err.Error(), "private question")
		})
	}
}

func TestAnswerMapsBareContextDeadlineToFallback(t *testing.T) {
	client := New(&answerClientStub{answer: func(context.Context, *answerv1.AnswerRequest, ...grpc.CallOption) (*answerv1.AnswerResponse, error) {
		return nil, context.DeadlineExceeded
	}}, time.Second)
	ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, testRequestID)

	_, err := client.Answer(ctx, handler.SearchRequest{Question: "private question"})

	assert.ErrorIs(t, err, handler.ErrAnswerUnavailable)
	assert.NotContains(t, err.Error(), "private question")
}

func TestAnswerRejectsInvalidRequestIDBeforeRPC(t *testing.T) {
	called := false
	client := New(&answerClientStub{answer: func(context.Context, *answerv1.AnswerRequest, ...grpc.CallOption) (*answerv1.AnswerResponse, error) {
		called = true
		return &answerv1.AnswerResponse{}, nil
	}}, time.Second)

	_, err := client.Answer(context.Background(), handler.SearchRequest{Question: "private question"})

	assert.ErrorIs(t, err, handler.ErrAnswerUnavailable)
	assert.False(t, called)
}

func TestAnswerNormalizesMissingAnswerAndEvidenceCollections(t *testing.T) {
	client := New(&answerClientStub{answer: func(context.Context, *answerv1.AnswerRequest, ...grpc.CallOption) (*answerv1.AnswerResponse, error) {
		return &answerv1.AnswerResponse{Search: &retrievalv1.SearchResponse{Query: "q"}}, nil
	}}, time.Second)
	ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, testRequestID)

	result, err := client.Answer(ctx, handler.SearchRequest{Question: "q"})

	require.NoError(t, err)
	assert.NotNil(t, result.Search.Results)
	assert.NotNil(t, result.Search.Documents)
	assert.Nil(t, result.Answer)
}
