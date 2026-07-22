// Package answerclient contains Edge's gRPC adapter for Answer.
package answerclient

import (
	"context"
	"errors"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/searchcontract"
)

const maxAnswerDeadline = 25 * time.Second

// Client translates Edge query requests to the versioned Answer contract.
type Client struct {
	service  answerv1.AnswerServiceClient
	deadline time.Duration
}

// New constructs an Answer client adapter with a bounded RPC deadline.
func New(service answerv1.AnswerServiceClient, deadline time.Duration) *Client {
	if service == nil {
		panic("answerclient: service must not be nil")
	}
	if deadline <= 0 || deadline > maxAnswerDeadline {
		panic("answerclient: deadline must be between zero and 25 seconds")
	}
	return &Client{service: service, deadline: deadline}
}

// Answer requests grounded synthesis while forwarding only trusted Edge data.
func (c *Client) Answer(ctx context.Context, request handler.SearchRequest) (handler.AnswerResult, error) {
	requestID := chimiddleware.GetReqID(ctx)
	if !searchcontract.ValidRequestID(requestID) {
		return handler.AnswerResult{}, handler.ErrAnswerUnavailable
	}
	metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
	metadata = metadata.Copy()
	metadata.Set("x-request-id", requestID)
	ctx = grpcmetadata.NewOutgoingContext(ctx, metadata)
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()

	response, err := c.service.Answer(ctx, &answerv1.AnswerRequest{
		Search: searchcontract.RequestToProto(request, requestID),
	})
	if err != nil {
		return handler.AnswerResult{}, mapError(err)
	}
	return resultFromProto(response), nil
}

func mapError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return handler.ErrAnswerUnavailable
	}
	switch status.Code(err) {
	case codes.InvalidArgument:
		return handler.ErrInvalidSearch
	case codes.PermissionDenied, codes.Unauthenticated:
		return handler.ErrSearchForbidden
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return handler.ErrAnswerUnavailable
	default:
		return handler.ErrAnswerFailed
	}
}

func resultFromProto(response *answerv1.AnswerResponse) handler.AnswerResult {
	if response == nil {
		return handler.AnswerResult{Search: searchcontract.ResultFromProto(nil)}
	}
	result := handler.AnswerResult{Search: searchcontract.ResultFromProto(response.Search)}
	if response.Answer == nil {
		return result
	}
	segments := make([]handler.AnswerSegment, 0, len(response.Answer.Segments))
	for _, segment := range response.Answer.Segments {
		if segment == nil {
			continue
		}
		segments = append(segments, handler.AnswerSegment{
			Text:        segment.Text,
			EvidenceIDs: append([]string{}, segment.EvidenceIds...),
		})
	}
	result.Answer = &handler.GroundedAnswer{Segments: segments}
	return result
}
