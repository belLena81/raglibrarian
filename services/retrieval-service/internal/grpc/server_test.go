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
	service := &stubSearchService{result: application.SearchResult{
		Evidence: []application.Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", MediaType: domain.MediaTypeEPUB, Passage: "Evidence", PageStart: 2, PageEnd: 3, Score: .8}},
		Documents: []application.DocumentResult{{DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", MediaType: domain.MediaTypeEPUB,
			ChunkCount: 2, PageStart: 1, PageEnd: 3, Score: .7, Evidence: []application.Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", MediaType: domain.MediaTypeEPUB, Passage: "Evidence"}}}},
	}}
	server := NewServer(service)
	response, err := server.Search(context.Background(), &retrievalv1.SearchRequest{Question: "replication", Limit: 2,
		Actor: &retrievalv1.Actor{UserId: "user-1", Role: "reader", Status: "active"}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Book.BookId != "book-1" ||
		response.Results[0].Book.MediaType != domain.MediaTypeEPUB || len(response.Documents) != 1 ||
		response.Documents[0].DocumentId != "document-1" || response.Documents[0].Book.MediaType != domain.MediaTypeEPUB {
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
	result application.SearchResult
	err    error
}

func (s *stubSearchService) Search(_ context.Context, _ domain.Actor, _ domain.SearchQueryInput) (application.SearchResult, error) {
	return s.result, s.err
}
