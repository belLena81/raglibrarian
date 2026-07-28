// Package retrievalclient contains Edge's gRPC adapter for Retrieval.
package retrievalclient

import (
	"context"
	"errors"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/rpcpolicy"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/searchcontract"
)

var (
	// ErrUnavailable is the sanitized Retrieval transport failure.
	ErrUnavailable = errors.New("retrieval unavailable")
)

// Client translates Edge search requests to the versioned Retrieval contract.
type Client struct {
	service retrievalv1.RetrievalServiceClient
	policy  Policy
}

// Policy defines runtime-tunable Retrieval RPC timing.
type Policy struct {
	ReadinessTimeout time.Duration
	SearchDeadline   time.Duration
}

// New constructs a Retrieval client adapter.
func New(service retrievalv1.RetrievalServiceClient, policy Policy) *Client {
	if service == nil {
		panic("retrievalclient: service must not be nil")
	}
	if policy.ReadinessTimeout <= 0 {
		panic("retrievalclient: readiness timeout must be positive")
	}
	if policy.SearchDeadline <= 0 || policy.SearchDeadline > rpcpolicy.MaximumRetrievalSearchDeadline {
		panic("retrievalclient: deadline must be between zero and 5 minutes")
	}
	return &Client{service: service, policy: policy}
}

// CheckReady verifies the synchronous Retrieval boundary within a short deadline.
func (c *Client) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.policy.ReadinessTimeout)
	defer cancel()
	_, err := c.service.Check(ctx, &retrievalv1.CheckRequest{})
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

// Search forwards one bounded authenticated search request.
func (c *Client) Search(ctx context.Context, request handler.SearchRequest) (handler.SearchResult, error) {
	requestID := chimiddleware.GetReqID(ctx)
	if !searchcontract.ValidRequestID(requestID) {
		return handler.SearchResult{}, ErrUnavailable
	}
	metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
	metadata = metadata.Copy()
	metadata.Set("x-request-id", requestID)
	ctx = grpcmetadata.NewOutgoingContext(ctx, metadata)
	ctx, cancel := context.WithTimeout(ctx, c.policy.SearchDeadline)
	defer cancel()

	response, err := c.service.Search(ctx, searchcontract.RequestToProto(request, requestID))
	if err != nil {
		return handler.SearchResult{}, mapError(err)
	}
	return searchcontract.ResultFromProto(response), nil
}

func mapError(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return handler.ErrInvalidSearch
	case codes.PermissionDenied, codes.Unauthenticated:
		return handler.ErrSearchForbidden
	default:
		return ErrUnavailable
	}
}
