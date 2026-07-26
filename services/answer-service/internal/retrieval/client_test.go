package retrieval

import (
	"context"
	"strings"
	"testing"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	"google.golang.org/grpc"
	grpcmetadata "google.golang.org/grpc/metadata"
)

type fakeRPC struct {
	request *retrievalv1.SearchRequest
	ids     []string
}

func (f *fakeRPC) Check(context.Context, *retrievalv1.CheckRequest, ...grpc.CallOption) (*retrievalv1.CheckResponse, error) {
	return &retrievalv1.CheckResponse{Status: "SERVING"}, nil
}
func (f *fakeRPC) Search(ctx context.Context, request *retrievalv1.SearchRequest, _ ...grpc.CallOption) (*retrievalv1.SearchResponse, error) {
	f.request = request
	metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
	f.ids = metadata.Get("x-request-id")
	return &retrievalv1.SearchResponse{
		Query: request.Question,
		Results: []*retrievalv1.Evidence{{
			EvidenceId: "e-1", Passage: "passage", Summary: "passage summary",
		}},
		Documents: []*retrievalv1.DocumentResult{{
			DocumentId: "doc-1",
			ChunkCount: 1,
			PageStart:  1,
			PageEnd:    2,
			Score:      0.9,
			Summary:    "document summary",
			Evidence: []*retrievalv1.Evidence{{
				EvidenceId: "e-1", Passage: "passage", Summary: "passage summary",
			}},
		}},
	}, nil
}

func TestClientForwardsActorCorrelationAndMapsEvidence(t *testing.T) {
	rpc := &fakeRPC{}
	client := NewClient(rpc)
	id := strings.Repeat("a", 32)
	result, err := client.Search(context.Background(), domain.SearchRequest{Question: "question", Limit: 5,
		Actor: domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: id})
	if err != nil || rpc.request.Actor.UserId != "user-1" || rpc.request.CorrelationId != id || len(rpc.ids) != 1 || rpc.ids[0] != id || result.Results[0].EvidenceID != "e-1" ||
		result.Results[0].Summary != "passage summary" || len(result.Documents) != 1 || result.Documents[0].Summary != "document summary" ||
		len(result.Documents[0].Evidence) != 1 || result.Documents[0].Evidence[0].Summary != "passage summary" {
		t.Fatalf("Search() = %#v, %v; request=%#v ids=%#v", result, err, rpc.request, rpc.ids)
	}
}
