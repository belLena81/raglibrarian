package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	querymiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

const (
	maxQueryQuestionLength = 2000
	maxQueryTags           = 20
	maxQueryTagLength      = 64
	maxQueryAuthorLength   = 256
	defaultQueryLimit      = 5
	maxQueryLimit          = 20
)

var (
	// ErrInvalidSearch identifies a request rejected by Retrieval validation.
	ErrInvalidSearch = errors.New("invalid search")
	// ErrSearchForbidden identifies a request denied by Retrieval authorization.
	ErrSearchForbidden = errors.New("search forbidden")
)

// QueryRequest is the bounded public request for POST /query.
type QueryRequest struct {
	Question string        `json:"question"`
	Filters  SearchFilters `json:"filters,omitempty"`
	Limit    int           `json:"limit,omitempty"`
}

// SearchFilters are optional metadata constraints forwarded to Retrieval.
type SearchFilters struct {
	Tags     []string `json:"tags,omitempty"`
	Author   string   `json:"author,omitempty"`
	YearFrom *int     `json:"year_from,omitempty"`
	YearTo   *int     `json:"year_to,omitempty"`
}

// SearchActor contains Identity-authoritative authorization facts.
type SearchActor struct {
	UserID string
	Role   string
	Status string
}

// SearchRequest is Edge's Retrieval client port input.
type SearchRequest struct {
	Question string
	Filters  SearchFilters
	Limit    int
	Actor    SearchActor
}

// EvidenceBook is the stored book projection attached to retrieved evidence.
type EvidenceBook struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Author string   `json:"author"`
	Year   int      `json:"year"`
	Tags   []string `json:"tags"`
}

// Evidence is one Retrieval-owned stored passage and its location.
type Evidence struct {
	EvidenceID string
	ChunkID    string
	Book       EvidenceBook
	Chapter    string
	Section    string
	PageStart  uint32
	PageEnd    uint32
	Passage    string
	Score      float64
}

// DocumentResult is one document-level hit with Retrieval-owned stored evidence.
type DocumentResult struct {
	DocumentID string
	Book       EvidenceBook
	ChunkCount uint32
	PageStart  uint32
	PageEnd    uint32
	Score      float64
	Evidence   []Evidence
}

// SearchResult contains only evidence returned by Retrieval.
type SearchResult struct {
	Query     string
	Results   []Evidence
	Documents []DocumentResult
}

// Searcher is the outbound Retrieval use-case port required by the handler.
type Searcher interface {
	Search(context.Context, SearchRequest) (SearchResult, error)
}

// QueryHandler exposes authenticated semantic evidence search.
type QueryHandler struct{ retrieval Searcher }

// NewQueryHandler constructs the semantic query boundary.
func NewQueryHandler(retrieval Searcher) *QueryHandler {
	if dependencyMissing(retrieval) {
		panic("handler: Retrieval must not be nil")
	}
	return &QueryHandler{retrieval: retrieval}
}

// Query validates the request and returns only Retrieval-provided evidence.
func (h *QueryHandler) Query(w http.ResponseWriter, r *http.Request) {
	var request QueryRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	principal, ok := querymiddleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if err := normalizeQueryRequest(&request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid query")
		return
	}

	result, err := h.retrieval.Search(r.Context(), SearchRequest{
		Question: request.Question,
		Filters:  request.Filters,
		Limit:    request.Limit,
		Actor: SearchActor{
			UserID: principal.UserID,
			Role:   principal.Role,
			Status: principal.Status,
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidSearch):
			writeError(w, http.StatusUnprocessableEntity, "invalid query")
		case errors.Is(err, ErrSearchForbidden):
			writeError(w, http.StatusForbidden, "forbidden")
		default:
			writeError(w, http.StatusServiceUnavailable, "retrieval is unavailable")
		}
		return
	}
	writeJSON(w, http.StatusOK, queryResponseFrom(result))
}

func normalizeQueryRequest(request *QueryRequest) error {
	request.Question = strings.TrimSpace(request.Question)
	request.Filters.Author = strings.TrimSpace(request.Filters.Author)
	if request.Question == "" || len(request.Question) > maxQueryQuestionLength || len(request.Filters.Author) > maxQueryAuthorLength {
		return ErrInvalidSearch
	}
	if len(request.Filters.Tags) > maxQueryTags {
		return ErrInvalidSearch
	}
	for index, tag := range request.Filters.Tags {
		request.Filters.Tags[index] = strings.TrimSpace(tag)
		if request.Filters.Tags[index] == "" || len(request.Filters.Tags[index]) > maxQueryTagLength {
			return ErrInvalidSearch
		}
	}
	if request.Filters.YearFrom != nil && request.Filters.YearTo != nil && *request.Filters.YearFrom > *request.Filters.YearTo {
		return ErrInvalidSearch
	}
	if !validQueryYear(request.Filters.YearFrom) || !validQueryYear(request.Filters.YearTo) {
		return ErrInvalidSearch
	}
	if request.Limit == 0 {
		request.Limit = defaultQueryLimit
	}
	if request.Limit < 1 || request.Limit > maxQueryLimit {
		return ErrInvalidSearch
	}
	return nil
}

func validQueryYear(year *int) bool {
	return year == nil || (*year >= 0 && *year <= 9999)
}

type queryResponse struct {
	Query     string             `json:"query"`
	Results   []evidenceResponse `json:"results"`
	Documents []documentResponse `json:"documents"`
}

type evidenceResponse struct {
	EvidenceID string       `json:"evidence_id"`
	ChunkID    string       `json:"chunk_id"`
	Book       EvidenceBook `json:"book"`
	Chapter    string       `json:"chapter"`
	Section    string       `json:"section"`
	Pages      [2]uint32    `json:"pages"`
	Passage    string       `json:"passage"`
	Score      float64      `json:"score"`
}

type documentResponse struct {
	DocumentID string             `json:"document_id"`
	Book       EvidenceBook       `json:"book"`
	ChunkCount uint32             `json:"chunk_count"`
	Pages      [2]uint32          `json:"pages"`
	Score      float64            `json:"score"`
	Evidence   []evidenceResponse `json:"evidence"`
}

func queryResponseFrom(result SearchResult) queryResponse {
	results := make([]evidenceResponse, 0, len(result.Results))
	for _, evidence := range result.Results {
		results = append(results, evidenceResponseFrom(evidence))
	}
	documents := make([]documentResponse, 0, len(result.Documents))
	for _, document := range result.Documents {
		evidence := make([]evidenceResponse, 0, len(document.Evidence))
		for _, value := range document.Evidence {
			evidence = append(evidence, evidenceResponseFrom(value))
		}
		documents = append(documents, documentResponse{DocumentID: document.DocumentID, Book: document.Book, ChunkCount: document.ChunkCount,
			Pages: [2]uint32{document.PageStart, document.PageEnd}, Score: document.Score, Evidence: evidence})
	}
	return queryResponse{Query: result.Query, Results: results, Documents: documents}
}

func evidenceResponseFrom(evidence Evidence) evidenceResponse {
	return evidenceResponse{
		EvidenceID: evidence.EvidenceID,
		ChunkID:    evidence.ChunkID,
		Book:       evidence.Book,
		Chapter:    evidence.Chapter,
		Section:    evidence.Section,
		Pages:      [2]uint32{evidence.PageStart, evidence.PageEnd},
		Passage:    evidence.Passage,
		Score:      evidence.Score,
	}
}
