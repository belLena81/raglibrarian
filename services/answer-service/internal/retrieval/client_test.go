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
	return &retrievalv1.SearchResponse{Query: request.Question, Results: []*retrievalv1.Evidence{{EvidenceId: "e-1", Passage: "passage"}}}, nil
}

func TestClientForwardsActorCorrelationAndMapsEvidence(t *testing.T) {
	rpc := &fakeRPC{}
	client := NewClient(rpc)
	id := strings.Repeat("a", 32)
	result, err := client.Search(context.Background(), domain.SearchRequest{Question: "question", Limit: 5,
		Actor: domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: id})
	if err != nil || rpc.request.Actor.UserId != "user-1" || rpc.request.CorrelationId != id || len(rpc.ids) != 1 || rpc.ids[0] != id || result.Results[0].EvidenceID != "e-1" {
		t.Fatalf("Search() = %#v, %v; request=%#v ids=%#v", result, err, rpc.request, rpc.ids)
	}
}
