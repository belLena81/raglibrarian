// Package answergrpc adapts the Answer protobuf contract to the application service.
package answergrpc

import (
	"context"
	"errors"

	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service interface {
	Answer(context.Context, domain.SearchRequest) (domain.AnswerResult, error)
	CheckReady(context.Context) error
}

type Server struct {
	answerv1.UnimplementedAnswerServiceServer
	service Service
}

func NewServer(service Service) *Server {
	if service == nil {
		panic("answergrpc: service is required")
	}
	return &Server{service: service}
}

func (s *Server) Check(ctx context.Context, _ *answerv1.CheckRequest) (*answerv1.CheckResponse, error) {
	if err := s.service.CheckReady(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, "answer unavailable")
	}
	return &answerv1.CheckResponse{Status: "SERVING"}, nil
}

func (s *Server) Answer(ctx context.Context, request *answerv1.AnswerRequest) (*answerv1.AnswerResponse, error) {
	if request == nil || request.Search == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid answer request")
	}
	result, err := s.service.Answer(ctx, searchFromProto(request.Search))
	if err != nil {
		return nil, mapError(err)
	}
	return responseToProto(result), nil
}

func searchFromProto(value *retrievalv1.SearchRequest) domain.SearchRequest {
	request := domain.SearchRequest{Question: value.Question, Limit: value.Limit, CorrelationID: value.CorrelationId}
	if value.Actor != nil {
		request.Actor = domain.Actor{UserID: value.Actor.UserId, Role: value.Actor.Role, Status: value.Actor.Status}
	}
	if value.Filters != nil {
		request.Filters = domain.SearchFilters{Tags: append([]string(nil), value.Filters.Tags...), Author: value.Filters.Author,
			YearFrom: value.Filters.YearFrom, YearTo: value.Filters.YearTo}
	}
	return request
}

func responseToProto(result domain.AnswerResult) *answerv1.AnswerResponse {
	response := &answerv1.AnswerResponse{Search: searchToProto(result.Search)}
	if result.Answer != nil {
		segments := make([]*answerv1.AnswerSegment, 0, len(result.Answer.Segments))
		for _, value := range result.Answer.Segments {
			segments = append(segments, &answerv1.AnswerSegment{Text: value.Text, EvidenceIds: append([]string(nil), value.EvidenceIDs...)})
		}
		response.Answer = &answerv1.GroundedAnswer{Segments: segments}
	}
	return response
}

func searchToProto(value domain.SearchResult) *retrievalv1.SearchResponse {
	response := &retrievalv1.SearchResponse{Query: value.Query, Results: make([]*retrievalv1.Evidence, 0, len(value.Results)), Documents: make([]*retrievalv1.DocumentResult, 0, len(value.Documents))}
	for _, evidence := range value.Results {
		response.Results = append(response.Results, evidenceToProto(evidence))
	}
	for _, document := range value.Documents {
		evidence := make([]*retrievalv1.Evidence, 0, len(document.Evidence))
		for _, item := range document.Evidence {
			evidence = append(evidence, evidenceToProto(item))
		}
		response.Documents = append(response.Documents, &retrievalv1.DocumentResult{DocumentId: document.DocumentID, Book: bookToProto(document.Book), ChunkCount: document.ChunkCount,
			PageStart: document.PageStart, PageEnd: document.PageEnd, Score: document.Score, Evidence: evidence})
	}
	return response
}

func evidenceToProto(value domain.Evidence) *retrievalv1.Evidence {
	return &retrievalv1.Evidence{EvidenceId: value.EvidenceID, ChunkId: value.ChunkID, Book: bookToProto(value.Book), Chapter: value.Chapter,
		Section: value.Section, PageStart: value.PageStart, PageEnd: value.PageEnd, Passage: value.Passage, Score: value.Score}
}

func bookToProto(value domain.BookMetadata) *retrievalv1.BookMetadata {
	return &retrievalv1.BookMetadata{BookId: value.BookID, Title: value.Title, Author: value.Author, Year: value.Year, Tags: append([]string(nil), value.Tags...)}
}

func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrForbidden):
		return status.Error(codes.PermissionDenied, "actor is not authorized")
	case errors.Is(err, domain.ErrInvalidRequest):
		return status.Error(codes.InvalidArgument, "invalid answer request")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "answer deadline exceeded")
	default:
		return status.Error(codes.Unavailable, "answer unavailable")
	}
}
