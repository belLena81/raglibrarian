package answergrpc

import (
	"context"
	"strings"
	"testing"

	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeService struct {
	result domain.AnswerResult
	err    error
}

func (f fakeService) Answer(context.Context, domain.SearchRequest) (domain.AnswerResult, error) {
	return f.result, f.err
}
func (f fakeService) CheckReady(context.Context) error { return f.err }

func TestServerMapsEvidenceAndAnswerWithoutLosingFields(t *testing.T) {
	result := domain.AnswerResult{Search: domain.SearchResult{
		Query: "q",
		Results: []domain.Evidence{{
			EvidenceID: "e-1", Passage: "p", Summary: "passage summary",
			Book: domain.BookMetadata{BookID: "book-1", Title: "Book", Author: "Author", Year: 2024, MediaType: "application/epub+zip"},
		}},
		Documents: []domain.DocumentResult{{
			DocumentID: "doc-1",
			ChunkCount: 1,
			PageStart:  1,
			PageEnd:    2,
			Score:      0.8,
			Summary:    "document summary",
			Evidence: []domain.Evidence{{
				EvidenceID: "e-1", Passage: "p", Summary: "passage summary",
				Book: domain.BookMetadata{BookID: "book-1", Title: "Book", Author: "Author", Year: 2024, MediaType: "application/epub+zip"},
			}},
			Book: domain.BookMetadata{BookID: "book-1", Title: "Book", Author: "Author", Year: 2024, MediaType: "application/epub+zip"},
		}},
	},
		Answer: &domain.GroundedAnswer{Segments: []domain.AnswerSegment{{Text: "a", EvidenceIDs: []string{"e-1"}}}}}
	server := NewServer(fakeService{result: result})
	response, err := server.Answer(context.Background(), validProtoRequest())
	if err != nil || response.Search.Results[0].Passage != "p" || response.Search.Results[0].Summary != "passage summary" ||
		response.Search.Results[0].Book.GetMediaType() != "application/epub+zip" || len(response.Search.Documents) != 1 || response.Search.Documents[0].Summary != "document summary" ||
		response.Search.Documents[0].Book.GetMediaType() != "application/epub+zip" || len(response.Search.Documents[0].Evidence) != 1 || response.Search.Documents[0].Evidence[0].Summary != "passage summary" ||
		response.Search.Documents[0].Evidence[0].Book.GetMediaType() != "application/epub+zip" ||
		response.Answer.Segments[0].EvidenceIds[0] != "e-1" {
		t.Fatalf("Answer() = %#v, %v", response, err)
	}
}

func TestServerRejectsMissingSearch(t *testing.T) {
	server := NewServer(fakeService{})
	_, err := server.Answer(context.Background(), &answerv1.AnswerRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v", status.Code(err))
	}
}

func validProtoRequest() *answerv1.AnswerRequest {
	return &answerv1.AnswerRequest{Search: &retrievalv1.SearchRequest{Question: "q", Limit: 5, CorrelationId: strings.Repeat("a", 32),
		Actor: &retrievalv1.Actor{UserId: "u", Role: "reader", Status: "active"}}}
}
