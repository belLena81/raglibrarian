package retrievalgrpc

import (
	"context"
	"testing"
	"time"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSearchMapsAuthorizedRequestAndEvidence(t *testing.T) {
	service := &stubSearchService{result: application.SearchResult{
		Evidence: []application.Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", MediaType: domain.MediaTypeEPUB, Passage: "Evidence", PageStart: 2, PageEnd: 3, Score: .8, Summary: "Evidence"}},
		Documents: []application.DocumentResult{{DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", MediaType: domain.MediaTypeEPUB,
			ChunkCount: 2, PageStart: 1, PageEnd: 3, Score: .7, Summary: "Evidence", Evidence: []application.Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", MediaType: domain.MediaTypeEPUB, Passage: "Evidence", Summary: "Evidence"}}}},
	}}
	server := NewServer(service, zap.NewNop(), 25*time.Second, 2*time.Second)
	response, err := server.Search(context.Background(), &retrievalv1.SearchRequest{Question: "replication", Limit: 2,
		Actor: &retrievalv1.Actor{UserId: "user-1", Role: "reader", Status: "active"}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Book.BookId != "book-1" ||
		response.Results[0].Book.MediaType != domain.MediaTypeEPUB || response.Results[0].Summary != "Evidence" || len(response.Documents) != 1 ||
		response.Documents[0].DocumentId != "document-1" || response.Documents[0].Book.MediaType != domain.MediaTypeEPUB || response.Documents[0].Summary != "Evidence" {
		t.Fatalf("Search() = %#v, %v", response, err)
	}
}

func TestSearchSanitizesAuthorizationFailure(t *testing.T) {
	server := NewServer(&stubSearchService{err: application.ErrSearchForbidden}, zap.NewNop(), 25*time.Second, 2*time.Second)
	_, err := server.Search(context.Background(), &retrievalv1.SearchRequest{Question: "secret", Actor: &retrievalv1.Actor{UserId: "user-1", Role: "reader", Status: "pending"}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Search() code = %v", status.Code(err))
	}
}

func TestSearchAppliesConfiguredDeadline(t *testing.T) {
	const timeout = 2 * time.Minute
	service := &stubSearchService{search: func(ctx context.Context, _ domain.Actor, _ domain.SearchQueryInput) (application.SearchResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("search context missing deadline")
		}
		remaining := time.Until(deadline)
		if remaining > timeout || remaining < timeout-5*time.Second {
			t.Fatalf("deadline remaining = %v, want about %v", remaining, timeout)
		}
		return application.SearchResult{}, nil
	}}
	server := NewServer(service, zap.NewNop(), timeout, 2*time.Second)

	if _, err := server.Search(context.Background(), &retrievalv1.SearchRequest{Question: "replication"}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
}

type stubSearchService struct {
	result application.SearchResult
	err    error
	search func(context.Context, domain.Actor, domain.SearchQueryInput) (application.SearchResult, error)
}

func (s *stubSearchService) Search(ctx context.Context, actor domain.Actor, input domain.SearchQueryInput) (application.SearchResult, error) {
	if s.search != nil {
		return s.search(ctx, actor, input)
	}
	return s.result, s.err
}
