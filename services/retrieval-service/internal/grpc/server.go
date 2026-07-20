// Package retrievalgrpc adapts the Retrieval protobuf contract to application use cases.
package retrievalgrpc

import (
	"context"
	"errors"
	"math"
	"time"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SearchService interface {
	Search(context.Context, domain.Actor, domain.SearchQueryInput) ([]application.Evidence, error)
}

type Server struct {
	retrievalv1.UnimplementedRetrievalServiceServer
	search    SearchService
	readiness []interface{ CheckReady(context.Context) error }
}

func NewServer(search SearchService, readiness ...interface{ CheckReady(context.Context) error }) *Server {
	if search == nil {
		panic("retrievalgrpc: search service is required")
	}
	return &Server{search: search, readiness: readiness}
}

func (s *Server) Check(ctx context.Context, _ *retrievalv1.CheckRequest) (*retrievalv1.CheckResponse, error) {
	if ctx.Err() != nil {
		return nil, status.Error(codes.Canceled, "request cancelled")
	}
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, dependency := range s.readiness {
		if dependency.CheckReady(probeContext) != nil {
			return nil, status.Error(codes.Unavailable, "retrieval unavailable")
		}
	}
	return &retrievalv1.CheckResponse{Status: "SERVING"}, nil
}

func (s *Server) Search(parent context.Context, request *retrievalv1.SearchRequest) (*retrievalv1.SearchResponse, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid search")
	}
	actor := domain.Actor{}
	if request.Actor != nil {
		actor = domain.Actor{UserID: request.Actor.UserId, Role: request.Actor.Role, Status: request.Actor.Status}
	}
	filters := domain.SearchFilters{}
	if request.Filters != nil {
		filters.Tags = append([]string(nil), request.Filters.Tags...)
		filters.Author = request.Filters.Author
		if request.Filters.YearFrom != nil {
			filters.YearFrom = int(*request.Filters.YearFrom)
		}
		if request.Filters.YearTo != nil {
			filters.YearTo = int(*request.Filters.YearTo)
		}
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	results, err := s.search.Search(ctx, actor, domain.SearchQueryInput{Question: request.Question, Filters: filters, Limit: int(request.Limit)})
	if err != nil {
		return nil, mapError(err)
	}
	response := &retrievalv1.SearchResponse{Query: request.Question, Results: make([]*retrievalv1.Evidence, 0, len(results))}
	for _, result := range results {
		if result.Year < 0 || result.Year > math.MaxInt32 {
			return nil, status.Error(codes.Unavailable, "retrieval unavailable")
		}
		response.Results = append(response.Results, &retrievalv1.Evidence{EvidenceId: result.EvidenceID, ChunkId: result.ChunkID,
			Book:    &retrievalv1.BookMetadata{BookId: result.BookID, Title: result.Title, Author: result.Author, Year: int32(result.Year), Tags: append([]string(nil), result.Tags...)}, // #nosec G115 -- range checked above.
			Chapter: result.Chapter, Section: result.Section, PageStart: result.PageStart, PageEnd: result.PageEnd, Passage: result.Passage, Score: result.Score})
	}
	return response, nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, application.ErrSearchForbidden):
		return status.Error(codes.PermissionDenied, "actor is not authorized")
	case errors.Is(err, domain.ErrInvalidSearchQuery):
		return status.Error(codes.InvalidArgument, "invalid search")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "search deadline exceeded")
	default:
		return status.Error(codes.Unavailable, "retrieval unavailable")
	}
}
