// Package retrieval adapts the Retrieval gRPC contract to Answer's application port.
package retrieval

import (
	"context"
	"errors"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	grpcmetadata "google.golang.org/grpc/metadata"
)

var ErrUnavailable = errors.New("retrieval unavailable")

type Client struct {
	rpc retrievalv1.RetrievalServiceClient
}

func NewClient(rpc retrievalv1.RetrievalServiceClient) *Client {
	if rpc == nil {
		panic("retrieval: RPC client is required")
	}
	return &Client{rpc: rpc}
}

func (c *Client) CheckReady(ctx context.Context) error {
	response, err := c.rpc.Check(ctx, &retrievalv1.CheckRequest{})
	if err != nil || response.GetStatus() != "SERVING" {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) Search(ctx context.Context, request domain.SearchRequest) (domain.SearchResult, error) {
	metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
	metadata = metadata.Copy()
	metadata.Set("x-request-id", request.CorrelationID)
	ctx = grpcmetadata.NewOutgoingContext(ctx, metadata)
	response, err := c.rpc.Search(ctx, toProto(request))
	if err != nil || response == nil {
		return domain.SearchResult{}, ErrUnavailable
	}
	return fromProto(response), nil
}

func toProto(request domain.SearchRequest) *retrievalv1.SearchRequest {
	filters := &retrievalv1.SearchFilters{Tags: append([]string(nil), request.Filters.Tags...), Author: request.Filters.Author,
		YearFrom: request.Filters.YearFrom, YearTo: request.Filters.YearTo}
	return &retrievalv1.SearchRequest{Question: request.Question, Filters: filters, Limit: request.Limit,
		Actor: &retrievalv1.Actor{UserId: request.Actor.UserID, Role: request.Actor.Role, Status: request.Actor.Status}, CorrelationId: request.CorrelationID}
}

func fromProto(response *retrievalv1.SearchResponse) domain.SearchResult {
	result := domain.SearchResult{Query: response.Query, Results: make([]domain.Evidence, 0, len(response.Results)), Documents: make([]domain.DocumentResult, 0, len(response.Documents))}
	for _, value := range response.Results {
		if value != nil {
			result.Results = append(result.Results, evidenceFromProto(value))
		}
	}
	for _, value := range response.Documents {
		if value == nil {
			continue
		}
		document := domain.DocumentResult{DocumentID: value.DocumentId, Book: bookFromProto(value.Book), ChunkCount: value.ChunkCount,
			PageStart: value.PageStart, PageEnd: value.PageEnd, Score: value.Score, Evidence: make([]domain.Evidence, 0, len(value.Evidence))}
		for _, evidence := range value.Evidence {
			if evidence != nil {
				document.Evidence = append(document.Evidence, evidenceFromProto(evidence))
			}
		}
		result.Documents = append(result.Documents, document)
	}
	return result
}

func evidenceFromProto(value *retrievalv1.Evidence) domain.Evidence {
	return domain.Evidence{EvidenceID: value.EvidenceId, ChunkID: value.ChunkId, Book: bookFromProto(value.Book), Chapter: value.Chapter,
		Section: value.Section, PageStart: value.PageStart, PageEnd: value.PageEnd, Passage: value.Passage, Score: value.Score}
}

func bookFromProto(value *retrievalv1.BookMetadata) domain.BookMetadata {
	if value == nil {
		return domain.BookMetadata{Tags: []string{}}
	}
	return domain.BookMetadata{BookID: value.BookId, Title: value.Title, Author: value.Author, Year: value.Year, Tags: append([]string(nil), value.Tags...)}
}
