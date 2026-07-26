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

// MaxAnswerDeadline bounds Edge's gRPC answer request budget.
const MaxAnswerDeadline = 5 * time.Minute

// Client translates Edge query requests to the versioned Answer contract.
type Client struct {
	service              answerv1.AnswerServiceClient
	deadline             time.Duration
	minimumEvidenceScore float64
}

// New constructs an Answer client adapter with a bounded RPC deadline.
func New(service answerv1.AnswerServiceClient, deadline time.Duration, minimumEvidenceScore float64) *Client {
	if service == nil {
		panic("answerclient: service must not be nil")
	}
	if deadline <= 0 || deadline > MaxAnswerDeadline {
		panic("answerclient: deadline must be between zero and 5 minutes")
	}
	if minimumEvidenceScore <= 0 || minimumEvidenceScore > 1 {
		panic("answerclient: minimum evidence score must be within (0, 1]")
	}
	return &Client{service: service, deadline: deadline, minimumEvidenceScore: minimumEvidenceScore}
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
		Search:               searchcontract.RequestToProto(request, requestID),
		MinimumEvidenceScore: c.minimumEvidenceScore,
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
	result := handler.AnswerResult{Search: searchcontract.ResultFromProto(response.Search), Summary: response.Summary}
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
