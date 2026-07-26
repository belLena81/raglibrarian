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
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SearchService interface {
	Search(context.Context, domain.Actor, domain.SearchQueryInput) (application.SearchResult, error)
}

type Server struct {
	retrievalv1.UnimplementedRetrievalServiceServer
	search    SearchService
	log       *zap.Logger
	timeout   time.Duration
	readiness []interface{ CheckReady(context.Context) error }
}

func NewServer(search SearchService, log *zap.Logger, timeout time.Duration, readiness ...interface{ CheckReady(context.Context) error }) *Server {
	if search == nil {
		panic("retrievalgrpc: search service is required")
	}
	if timeout <= 0 {
		panic("retrievalgrpc: search timeout is required")
	}
	return &Server{search: search, log: log, timeout: timeout, readiness: readiness}
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
	started := time.Now()
	if s.log != nil {
		s.log.Info("retrieval.query.received",
			zap.Int("question_length", len(request.Question)),
			zap.Uint32("limit", request.Limit),
			zap.String("actor_role", actorRole(request.Actor)),
			zap.String("actor_status", actorStatus(request.Actor)),
		)
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
			value := int(*request.Filters.YearFrom)
			filters.YearFrom = &value
		}
		if request.Filters.YearTo != nil {
			value := int(*request.Filters.YearTo)
			filters.YearTo = &value
		}
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	results, err := s.search.Search(ctx, actor, domain.SearchQueryInput{Question: request.Question, Filters: filters, Limit: int(request.Limit)})
	if err != nil {
		if s.log != nil {
			s.log.Warn("retrieval.query.failed",
				zap.String("reason_code", errorReason(err)),
				zap.Int64("duration_ms", time.Since(started).Milliseconds()),
			)
		}
		return nil, mapError(err)
	}
	response := &retrievalv1.SearchResponse{Query: request.Question, Results: make([]*retrievalv1.Evidence, 0, len(results.Evidence)), Documents: make([]*retrievalv1.DocumentResult, 0, len(results.Documents))}
	for _, result := range results.Evidence {
		evidence, mapErr := evidenceToProto(result)
		if mapErr != nil {
			return nil, mapErr
		}
		response.Results = append(response.Results, evidence)
	}
	for _, result := range results.Documents {
		if result.Year < 0 || result.Year > math.MaxInt32 {
			return nil, status.Error(codes.Unavailable, "retrieval unavailable")
		}
		evidence := make([]*retrievalv1.Evidence, 0, len(result.Evidence))
		for _, value := range result.Evidence {
			mapped, mapErr := evidenceToProto(value)
			if mapErr != nil {
				return nil, mapErr
			}
			evidence = append(evidence, mapped)
		}
		response.Documents = append(response.Documents, &retrievalv1.DocumentResult{DocumentId: result.DocumentID,
			Book:       &retrievalv1.BookMetadata{BookId: result.BookID, Title: result.Title, Author: result.Author, Year: int32(result.Year), Tags: append([]string(nil), result.Tags...), MediaType: result.MediaType}, // #nosec G115 -- range checked above.
			ChunkCount: result.ChunkCount, PageStart: result.PageStart, PageEnd: result.PageEnd, Score: result.Score, Evidence: evidence, Summary: result.Summary})
	}
	if s.log != nil {
		s.log.Info("retrieval.query.completed",
			zap.Int("evidence_count", len(response.Results)),
			zap.Int("document_count", len(response.Documents)),
			zap.Int64("duration_ms", time.Since(started).Milliseconds()),
		)
	}
	return response, nil
}

func actorRole(actor *retrievalv1.Actor) string {
	if actor == nil {
		return ""
	}
	return actor.Role
}

func actorStatus(actor *retrievalv1.Actor) string {
	if actor == nil {
		return ""
	}
	return actor.Status
}

func evidenceToProto(result application.Evidence) (*retrievalv1.Evidence, error) {
	if result.Year < 0 || result.Year > math.MaxInt32 {
		return nil, status.Error(codes.Unavailable, "retrieval unavailable")
	}
	return &retrievalv1.Evidence{EvidenceId: result.EvidenceID, ChunkId: result.ChunkID,
		Book:    &retrievalv1.BookMetadata{BookId: result.BookID, Title: result.Title, Author: result.Author, Year: int32(result.Year), Tags: append([]string(nil), result.Tags...), MediaType: result.MediaType}, // #nosec G115 -- range checked above.
		Chapter: result.Chapter, Section: result.Section, PageStart: result.PageStart, PageEnd: result.PageEnd, Passage: result.Passage, Score: result.Score, Summary: result.Summary}, nil
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

func errorReason(err error) string {
	switch {
	case errors.Is(err, application.ErrSearchForbidden):
		return "search_forbidden"
	case errors.Is(err, domain.ErrInvalidSearchQuery):
		return "invalid_search"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "retrieval_unavailable"
	}
}
