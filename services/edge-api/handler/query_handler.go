package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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
	// ErrAnswerUnavailable identifies a retryable Answer transport failure that
	// permits one direct Retrieval fallback.
	ErrAnswerUnavailable = errors.New("answer unavailable")
	// ErrAnswerFailed identifies a non-retryable Answer failure.
	ErrAnswerFailed = errors.New("answer failed")
)

// QueryRequest is the bounded public request for POST /query.
type QueryRequest struct {
	Question string        `json:"question"`
	Mode     string        `json:"mode,omitempty"`
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

// AnswerSegment is one validated piece of generated text and its evidence IDs.
type AnswerSegment struct {
	Text        string
	EvidenceIDs []string
}

// GroundedAnswer contains only segments validated by Answer.
type GroundedAnswer struct {
	Segments []AnswerSegment
}

// AnswerResult preserves Retrieval's evidence and may include synthesis.
type AnswerResult struct {
	Search SearchResult
	Answer *GroundedAnswer
}

// Searcher is the outbound Retrieval use-case port required by the handler.
type Searcher interface {
	Search(context.Context, SearchRequest) (SearchResult, error)
}

// Answerer is the outbound grounded-answer use-case port required in answer mode.
type Answerer interface {
	Answer(context.Context, SearchRequest) (AnswerResult, error)
}

// AnswerAdmission applies the stricter per-principal answer-mode limit.
type AnswerAdmission interface {
	Allow(userID, role string) (bool, time.Duration)
}

// QueryHandler exposes authenticated semantic evidence search.
type QueryHandler struct {
	retrieval       Searcher
	answer          Answerer
	answerAdmission AnswerAdmission
}

// QueryHandlerOption configures optional query capabilities.
type QueryHandlerOption func(*QueryHandler)

// WithAnswer enables grounded-answer mode and its independent admission limit.
func WithAnswer(answer Answerer, admission AnswerAdmission) QueryHandlerOption {
	if dependencyMissing(answer) || dependencyMissing(admission) {
		panic("handler: Answer and admission control must not be nil")
	}
	return func(handler *QueryHandler) {
		handler.answer = answer
		handler.answerAdmission = admission
	}
}

// NewQueryHandler constructs the semantic query boundary.
func NewQueryHandler(retrieval Searcher, options ...QueryHandlerOption) *QueryHandler {
	if dependencyMissing(retrieval) {
		panic("handler: Retrieval must not be nil")
	}
	handler := &QueryHandler{retrieval: retrieval}
	for _, option := range options {
		if option == nil {
			panic("handler: query option must not be nil")
		}
		option(handler)
	}
	return handler
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

	searchRequest := SearchRequest{
		Question: request.Question,
		Filters:  request.Filters,
		Limit:    request.Limit,
		Actor: SearchActor{
			UserID: principal.UserID,
			Role:   principal.Role,
			Status: principal.Status,
		},
	}
	if request.Mode == "answer" && h.answer != nil {
		if allowed, retryAfter := h.answerAdmission.Allow(principal.UserID, principal.Role); !allowed {
			querymiddleware.WriteRateLimited(w, r, retryAfter)
			return
		}
		result, err := h.answer.Answer(r.Context(), searchRequest)
		if err == nil {
			writeJSON(w, http.StatusOK, queryResponseFromAnswer(result))
			return
		}
		if !errors.Is(err, ErrAnswerUnavailable) {
			writeQueryError(w, err)
			return
		}
	}

	result, err := h.retrieval.Search(r.Context(), searchRequest)
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queryResponseFrom(result))
}

func writeQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidSearch):
		writeError(w, http.StatusUnprocessableEntity, "invalid query")
	case errors.Is(err, ErrSearchForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, ErrAnswerFailed):
		writeError(w, http.StatusServiceUnavailable, "answer is unavailable")
	default:
		writeError(w, http.StatusServiceUnavailable, "retrieval is unavailable")
	}
}

func normalizeQueryRequest(request *QueryRequest) error {
	request.Question = strings.TrimSpace(request.Question)
	request.Mode = strings.TrimSpace(request.Mode)
	if request.Mode == "" {
		request.Mode = "search"
	}
	if request.Mode != "search" && request.Mode != "answer" {
		return ErrInvalidSearch
	}
	request.Filters.Author = strings.TrimSpace(request.Filters.Author)
	if request.Question == "" || utf8.RuneCountInString(request.Question) > maxQueryQuestionLength || utf8.RuneCountInString(request.Filters.Author) > maxQueryAuthorLength {
		return ErrInvalidSearch
	}
	if len(request.Filters.Tags) > maxQueryTags {
		return ErrInvalidSearch
	}
	for index, tag := range request.Filters.Tags {
		request.Filters.Tags[index] = strings.TrimSpace(tag)
		if request.Filters.Tags[index] == "" || utf8.RuneCountInString(request.Filters.Tags[index]) > maxQueryTagLength {
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
	Answer    *answerResponse    `json:"answer,omitempty"`
}

type answerResponse struct {
	Segments []answerSegmentResponse `json:"segments"`
}

type answerSegmentResponse struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidence_ids"`
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

func queryResponseFromAnswer(result AnswerResult) queryResponse {
	response := queryResponseFrom(result.Search)
	if result.Answer == nil {
		return response
	}
	segments := make([]answerSegmentResponse, 0, len(result.Answer.Segments))
	for _, segment := range result.Answer.Segments {
		segments = append(segments, answerSegmentResponse{
			Text:        segment.Text,
			EvidenceIDs: append([]string{}, segment.EvidenceIDs...),
		})
	}
	response.Answer = &answerResponse{Segments: segments}
	return response
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
