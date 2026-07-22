// Package searchcontract maps Edge's search model to the shared Retrieval
// contract for outbound adapters.
package searchcontract

import (
	"encoding/hex"
	"strings"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

// ValidRequestID reports whether value is an Edge-generated request ID.
func ValidRequestID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

// RequestToProto maps Edge's trusted search request to Retrieval's contract.
func RequestToProto(request handler.SearchRequest, correlationID string) *retrievalv1.SearchRequest {
	filters := &retrievalv1.SearchFilters{
		Tags:   append([]string(nil), request.Filters.Tags...),
		Author: request.Filters.Author,
	}
	if request.Filters.YearFrom != nil {
		value := int32(*request.Filters.YearFrom) // #nosec G115 -- public validation bounds years to four digits.
		filters.YearFrom = &value
	}
	if request.Filters.YearTo != nil {
		value := int32(*request.Filters.YearTo) // #nosec G115 -- public validation bounds years to four digits.
		filters.YearTo = &value
	}
	return &retrievalv1.SearchRequest{
		Question: request.Question,
		Filters:  filters,
		Limit:    uint32(request.Limit), // #nosec G115 -- public validation limits values to 1..20.
		Actor: &retrievalv1.Actor{
			UserId: request.Actor.UserID,
			Role:   request.Actor.Role,
			Status: request.Actor.Status,
		},
		CorrelationId: correlationID,
	}
}

// ResultFromProto maps Retrieval-owned evidence into Edge's HTTP model.
func ResultFromProto(response *retrievalv1.SearchResponse) handler.SearchResult {
	if response == nil {
		return handler.SearchResult{Results: []handler.Evidence{}, Documents: []handler.DocumentResult{}}
	}
	results := make([]handler.Evidence, 0, len(response.Results))
	for _, evidence := range response.Results {
		if evidence != nil {
			results = append(results, evidenceFromProto(evidence))
		}
	}
	documents := make([]handler.DocumentResult, 0, len(response.Documents))
	for _, document := range response.Documents {
		if document == nil {
			continue
		}
		evidence := make([]handler.Evidence, 0, len(document.Evidence))
		for _, value := range document.Evidence {
			if value != nil {
				evidence = append(evidence, evidenceFromProto(value))
			}
		}
		documents = append(documents, handler.DocumentResult{
			DocumentID: document.DocumentId,
			Book:       bookFromProto(document.Book),
			ChunkCount: document.ChunkCount,
			PageStart:  document.PageStart,
			PageEnd:    document.PageEnd,
			Score:      document.Score,
			Evidence:   evidence,
		})
	}
	return handler.SearchResult{Query: response.Query, Results: results, Documents: documents}
}

func evidenceFromProto(evidence *retrievalv1.Evidence) handler.Evidence {
	return handler.Evidence{
		EvidenceID: evidence.EvidenceId,
		ChunkID:    evidence.ChunkId,
		Book:       bookFromProto(evidence.Book),
		Chapter:    evidence.Chapter,
		Section:    evidence.Section,
		PageStart:  evidence.PageStart,
		PageEnd:    evidence.PageEnd,
		Passage:    evidence.Passage,
		Score:      evidence.Score,
	}
}

func bookFromProto(book *retrievalv1.BookMetadata) handler.EvidenceBook {
	if book == nil {
		return handler.EvidenceBook{Tags: []string{}}
	}
	return handler.EvidenceBook{
		ID:     book.BookId,
		Title:  book.Title,
		Author: book.Author,
		Year:   int(book.Year),
		Tags:   append([]string{}, book.Tags...),
	}
}
