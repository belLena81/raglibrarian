// Package retrievalclient contains Edge's gRPC adapter for Retrieval.
package retrievalclient

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

var (
	// ErrUnavailable is the sanitized Retrieval transport failure.
	ErrUnavailable = errors.New("retrieval unavailable")
)

// Client translates Edge search requests to the versioned Retrieval contract.
type Client struct {
	service retrievalv1.RetrievalServiceClient
}

// New constructs a Retrieval client adapter.
func New(service retrievalv1.RetrievalServiceClient) *Client {
	if service == nil {
		panic("retrievalclient: service must not be nil")
	}
	return &Client{service: service}
}

// CheckReady verifies the synchronous Retrieval boundary within a short deadline.
func (c *Client) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
	if !validRequestID(requestID) {
		return handler.SearchResult{}, ErrUnavailable
	}
	metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
	metadata = metadata.Copy()
	metadata.Set("x-request-id", requestID)
	ctx = grpcmetadata.NewOutgoingContext(ctx, metadata)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	filters := &retrievalv1.SearchFilters{
		Tags:   append([]string(nil), request.Filters.Tags...),
		Author: request.Filters.Author,
	}
	if request.Filters.YearFrom != nil {
		value := int32(*request.Filters.YearFrom) // #nosec G115 -- public validation bounds years to four digits.
		filters.YearFrom = &value
	}
	if request.Filters.YearTo != nil {
		value := int32(*request.Filters.YearTo) // #nosec G115 -- public validation bounds years to four digits.
		filters.YearTo = &value
	}
	response, err := c.service.Search(ctx, &retrievalv1.SearchRequest{
		Question: request.Question,
		Filters:  filters,
		Limit:    uint32(request.Limit), // #nosec G115 -- public validation limits values to 1..20.
		Actor: &retrievalv1.Actor{
			UserId: request.Actor.UserID,
			Role:   request.Actor.Role,
			Status: request.Actor.Status,
		},
		CorrelationId: requestID,
	})
	if err != nil {
		return handler.SearchResult{}, mapError(err)
	}
	return fromProto(response), nil
}

func validRequestID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
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

func fromProto(response *retrievalv1.SearchResponse) handler.SearchResult {
	if response == nil {
		return handler.SearchResult{Results: []handler.Evidence{}, Documents: []handler.DocumentResult{}}
	}
	results := make([]handler.Evidence, 0, len(response.Results))
	for _, evidence := range response.Results {
		if evidence == nil {
			continue
		}
		results = append(results, evidenceFromProto(evidence))
	}
	documents := make([]handler.DocumentResult, 0, len(response.Documents))
	for _, document := range response.Documents {
		if document == nil {
			continue
		}
		evidence := make([]handler.Evidence, 0, len(document.Evidence))
		for _, value := range document.Evidence {
			if value != nil {
				evidence = append(evidence, evidenceFromProto(value))
			}
		}
		documents = append(documents, handler.DocumentResult{DocumentID: document.DocumentId, Book: bookFromProto(document.Book),
			ChunkCount: document.ChunkCount, PageStart: document.PageStart, PageEnd: document.PageEnd, Score: document.Score, Evidence: evidence})
	}
	return handler.SearchResult{Query: response.Query, Results: results, Documents: documents}
}

func evidenceFromProto(evidence *retrievalv1.Evidence) handler.Evidence {
	return handler.Evidence{
		EvidenceID: evidence.EvidenceId,
		ChunkID:    evidence.ChunkId,
		Book:       bookFromProto(evidence.Book),
		Chapter:    evidence.Chapter,
		Section:    evidence.Section,
		PageStart:  evidence.PageStart,
		PageEnd:    evidence.PageEnd,
		Passage:    evidence.Passage,
		Score:      evidence.Score,
	}
}

func bookFromProto(book *retrievalv1.BookMetadata) handler.EvidenceBook {
	if book == nil {
		return handler.EvidenceBook{Tags: []string{}}
	}
	return handler.EvidenceBook{
		ID:     book.BookId,
		Title:  book.Title,
		Author: book.Author,
		Year:   int(book.Year),
		Tags:   append([]string(nil), book.Tags...),
	}
}
