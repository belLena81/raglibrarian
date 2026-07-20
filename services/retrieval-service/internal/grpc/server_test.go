package retrievalgrpc

import (
	"context"
	"testing"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSearchMapsAuthorizedRequestAndEvidence(t *testing.T) {
	service := &stubSearchService{results: []application.Evidence{{EvidenceID: "evidence-1", BookID: "book-1", Title: "Systems", Passage: "Evidence", PageStart: 2, PageEnd: 3, Score: .8}}}
	server := NewServer(service)
	response, err := server.Search(context.Background(), &retrievalv1.SearchRequest{Question: "replication", Limit: 2,
		Actor: &retrievalv1.Actor{UserId: "user-1", Role: "reader", Status: "active"}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Book.BookId != "book-1" {
		t.Fatalf("Search() = %#v, %v", response, err)
	}
}

func TestSearchSanitizesAuthorizationFailure(t *testing.T) {
	server := NewServer(&stubSearchService{err: application.ErrSearchForbidden})
	_, err := server.Search(context.Background(), &retrievalv1.SearchRequest{Question: "secret", Actor: &retrievalv1.Actor{UserId: "user-1", Role: "reader", Status: "pending"}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Search() code = %v", status.Code(err))
	}
}

type stubSearchService struct {
	results []application.Evidence
	err     error
}

func (s *stubSearchService) Search(_ context.Context, _ domain.Actor, _ domain.SearchQueryInput) ([]application.Evidence, error) {
	return s.results, s.err
}
