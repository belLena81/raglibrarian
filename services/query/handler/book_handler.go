package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/domain"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
)

const (
	// maxBodyBytes caps the request body to prevent slow-client / memory exhaustion.
	maxBodyBytes = 1 << 20 // 1 MiB — far above any valid book JSON payload

	// defaultListLimit and maxListLimit bound the pagination window.
	// maxListLimit is enforced server-side so clients cannot opt out.
	defaultListLimit = 50
	maxListLimit     = 200
)

// BookUseCase is the subset of metausecase.BookUseCase required by BookHandler.
// Declared locally so (a) the handler can be tested with a trivial fake and
// (b) the dependency surface of the HTTP layer is explicit.
// The production *metausecase.BookService satisfies it structurally.
type BookUseCase interface {
	AddBook(ctx context.Context, title, author string, year int) (domain.Book, error)
	GetBook(ctx context.Context, id string) (domain.Book, error)
	ListBooks(ctx context.Context, f metarepo.ListFilter) ([]domain.Book, error)
	RemoveBook(ctx context.Context, id string) error
	TriggerReindex(ctx context.Context, id string) error
}

// BookHandler handles the /admin/books/* routes.
// All routes require at least RoleLibrarian — enforced by RequireMinRole in
// the router. The handler does not repeat the role check.
type BookHandler struct {
	uc  BookUseCase
	log *zap.Logger
}

// NewBookHandler constructs a BookHandler. Panics on nil deps.
func NewBookHandler(uc BookUseCase, log *zap.Logger) *BookHandler {
	if uc == nil {
		panic("handler: bookUseCase must not be nil")
	}
	if log == nil {
		panic("handler: Logger must not be nil")
	}
	return &BookHandler{uc: uc, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

// AddBookRequest is the JSON body for POST /admin/books.
type AddBookRequest struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	Year   int    `json:"year"`
}

// BookResponse is the JSON representation of a book returned by all endpoints.
// s3_key is omitted when empty — it is populated later by the ingest Lambda.
// Timestamps are RFC 3339 UTC strings; using a fixed layout avoids timezone
// ambiguity for consumers that do not parse time.Time natively.
type BookResponse struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Author      string   `json:"author"`
	Year        int      `json:"year"`
	IndexStatus string   `json:"index_status"`
	Tags        []string `json:"tags"`
	S3Key       string   `json:"s3_key,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// ListBooksResponse is returned by GET /admin/books.
// Total is the count before pagination so clients can compute page counts.
type ListBooksResponse struct {
	Books  []BookResponse `json:"books"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// AddBook handles POST /admin/books.
// Creates a book in StatusPending and publishes EventBookCreated.
// Returns 201 Created with the full book representation.
func (h *BookHandler) AddBook(w http.ResponseWriter, r *http.Request) {
	reqID := chimiddleware.GetReqID(r.Context())

	var req AddBookRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeBookError(w, err)
		return
	}

	// Validate required fields at the HTTP boundary to return a 422 with a
	// specific message rather than surfacing a domain sentinel string verbatim.
	if strings.TrimSpace(req.Title) == "" {
		writeBookErrorMsg(w, http.StatusUnprocessableEntity, "title is required")
		return
	}
	if strings.TrimSpace(req.Author) == "" {
		writeBookErrorMsg(w, http.StatusUnprocessableEntity, "author is required")
		return
	}
	if req.Year == 0 {
		writeBookErrorMsg(w, http.StatusUnprocessableEntity, "year is required")
		return
	}

	book, err := h.uc.AddBook(r.Context(), req.Title, req.Author, req.Year)
	if err != nil {
		h.log.Debug("AddBook failed",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeBookError(w, err)
		return
	}

	writeBookJSON(w, http.StatusCreated, toBookResponse(book))
}

// GetBook handles GET /admin/books/{id}.
func (h *BookHandler) GetBook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	book, err := h.uc.GetBook(r.Context(), id)
	if err != nil {
		h.log.Debug("GetBook failed",
			zap.String("request_id", chimiddleware.GetReqID(r.Context())),
			zap.String("book_id", id),
			zap.Error(err),
		)
		writeBookError(w, err)
		return
	}

	writeBookJSON(w, http.StatusOK, toBookResponse(book))
}

// ListBooks handles GET /admin/books.
//
// Query parameters (all optional):
//
//	author        — exact author match
//	year_from     — publication year lower bound (inclusive)
//	year_to       — publication year upper bound (inclusive)
//	tags          — comma-separated; book must contain ALL supplied tags
//	index_status  — pending | indexing | indexed | failed
//	limit         — page size (default 50, max 200)
//	offset        — records to skip (default 0)
func (h *BookHandler) ListBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var filter metarepo.ListFilter

	if author := q.Get("author"); author != "" {
		filter.Author = &author
	}

	if yf := q.Get("year_from"); yf != "" {
		yr, err := strconv.Atoi(yf)
		if err != nil {
			writeBookErrorMsg(w, http.StatusUnprocessableEntity, "year_from must be an integer")
			return
		}
		filter.YearFrom = &yr
	}

	if yt := q.Get("year_to"); yt != "" {
		yr, err := strconv.Atoi(yt)
		if err != nil {
			writeBookErrorMsg(w, http.StatusUnprocessableEntity, "year_to must be an integer")
			return
		}
		filter.YearTo = &yr
	}

	if tagsParam := q.Get("tags"); tagsParam != "" {
		raw := strings.Split(tagsParam, ",")
		tags := make([]string, 0, len(raw))
		for _, t := range raw {
			if s := strings.TrimSpace(t); s != "" {
				tags = append(tags, s)
			}
		}
		if len(tags) > 0 {
			filter.Tags = tags
		}
	}

	if statusParam := q.Get("index_status"); statusParam != "" {
		s, err := domain.StatusValueOf(statusParam)
		if err != nil {
			writeBookErrorMsg(w, http.StatusUnprocessableEntity,
				"index_status must be one of: pending, indexing, indexed, failed")
			return
		}
		filter.Status = &s
	}

	limit, offset, err := parsePagination(q.Get("limit"), q.Get("offset"))
	if err != nil {
		writeBookErrorMsg(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	books, err := h.uc.ListBooks(r.Context(), filter)
	if err != nil {
		h.log.Error("ListBooks failed",
			zap.String("request_id", chimiddleware.GetReqID(r.Context())),
			zap.Error(err),
		)
		writeBookError(w, err)
		return
	}

	// Apply pagination in-process. The library is expected to be small (hundreds
	// of books) so a full-table fetch is acceptable. Move the slice into the
	// repository query when this becomes a bottleneck.
	total := len(books)
	start := clamp(offset, 0, total)
	end := clamp(start+limit, 0, total)
	page := books[start:end]

	dtos := make([]BookResponse, 0, len(page))
	for _, b := range page {
		dtos = append(dtos, toBookResponse(b))
	}

	writeBookJSON(w, http.StatusOK, ListBooksResponse{
		Books:  dtos,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// RemoveBook handles DELETE /admin/books/{id}.
// Returns 204 No Content on success.
func (h *BookHandler) RemoveBook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.uc.RemoveBook(r.Context(), id); err != nil {
		h.log.Debug("RemoveBook failed",
			zap.String("request_id", chimiddleware.GetReqID(r.Context())),
			zap.String("book_id", id),
			zap.Error(err),
		)
		writeBookError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// TriggerReindex handles POST /admin/books/{id}/reindex.
// Resets a terminal book to StatusPending and publishes EventBookReindexRequested.
// Returns 202 Accepted — reindex runs asynchronously via the Lambda pipeline.
// Clients should poll GET /admin/books/{id} to observe the status progression.
func (h *BookHandler) TriggerReindex(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.uc.TriggerReindex(r.Context(), id); err != nil {
		h.log.Debug("TriggerReindex failed",
			zap.String("request_id", chimiddleware.GetReqID(r.Context())),
			zap.String("book_id", id),
			zap.Error(err),
		)
		writeBookError(w, err)
		return
	}

	type reindexResponse struct {
		Message string `json:"message"`
		BookID  string `json:"book_id"`
	}
	writeBookJSON(w, http.StatusAccepted, reindexResponse{
		Message: "reindex triggered",
		BookID:  id,
	})
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

// httpError carries a pre-determined HTTP status and a client-safe message.
// It is returned by decodeJSONBody so writeBookError can dispatch correctly
// without a separate type-switch at every call site.
type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

// decodeJSONBody reads the request body into dst with three hardened guards:
//  1. Content-Type enforcement: must be application/json (ignores parameters
//     like charset). Returns 415 Unsupported Media Type otherwise.
//  2. Body size cap via http.MaxBytesReader. Returns 413 if exceeded.
//  3. DisallowUnknownFields: returns 400 if the client sends unexpected keys,
//     which prevents silent field-name typos from going undetected.
//
// The w parameter is required by http.MaxBytesReader.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	if mediaType != "application/json" {
		return httpError{
			status: http.StatusUnsupportedMediaType,
			msg:    "Content-Type must be application/json",
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return httpError{status: http.StatusRequestEntityTooLarge, msg: "request body too large"}
		case errors.Is(err, io.EOF):
			return httpError{status: http.StatusBadRequest, msg: "request body must not be empty"}
		default:
			return httpError{status: http.StatusBadRequest, msg: "invalid JSON: " + sanitiseDecodeErr(err)}
		}
	}
	return nil
}

// sanitiseDecodeErr trims the offset information that json.Decoder appends to
// error messages. The offset is an internal implementation detail that has no
// meaning to API clients and may reveal payload structure.
func sanitiseDecodeErr(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, " at offset"); i > 0 {
		return msg[:i]
	}
	return msg
}

// writeBookError dispatches to writeBookErrorMsg using the correct status code.
// httpError (from decodeJSONBody) takes precedence over domain error mapping.
func writeBookError(w http.ResponseWriter, err error) {
	if he, ok := errors.AsType[httpError](err); ok {
		writeBookErrorMsg(w, he.status, he.msg)
		return
	}
	writeBookErrorMsg(w, bookErrToStatus(err), bookErrToMsg(err))
}

// bookErrToStatus maps domain and use-case errors to HTTP status codes.
func bookErrToStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrBookNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrDuplicateBook):
		return http.StatusConflict
	case errors.Is(err, domain.ErrInvalidStatusTransition):
		// 409 Conflict: the request is valid but the current resource state
		// prevents it. Retrying after the status changes may succeed.
		return http.StatusConflict
	case errors.Is(err, domain.ErrEmptyTitle),
		errors.Is(err, domain.ErrEmptyAuthor),
		errors.Is(err, domain.ErrInvalidYear),
		errors.Is(err, domain.ErrEmptyBookID),
		errors.Is(err, domain.ErrInvalidStatus),
		errors.Is(err, domain.ErrInvalidTag):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// bookErrToMsg returns a client-safe message for each known error.
// 5xx errors get a generic message to avoid leaking infrastructure details.
func bookErrToMsg(err error) string {
	switch {
	case errors.Is(err, domain.ErrBookNotFound):
		return "book not found"
	case errors.Is(err, domain.ErrDuplicateBook):
		return "a book with this title, author, and year already exists"
	case errors.Is(err, domain.ErrInvalidStatusTransition):
		return "book is not in a state that allows this operation"
	case errors.Is(err, domain.ErrEmptyTitle):
		return "title is required"
	case errors.Is(err, domain.ErrEmptyAuthor):
		return "author is required"
	case errors.Is(err, domain.ErrInvalidYear):
		return "year must be between 1900 and the current year"
	case errors.Is(err, domain.ErrInvalidTag):
		return "tags must not be empty or contain duplicates"
	default:
		return "internal server error"
	}
}

func writeBookErrorMsg(w http.ResponseWriter, status int, msg string) {
	type errBody struct {
		Error string `json:"error"`
	}
	writeBookJSON(w, status, errBody{Error: msg})
}

func writeBookJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// toBookResponse converts a domain.Book value to its wire representation.
// RFC 3339 UTC timestamps are formatted with a fixed layout so serialisation
// is deterministic regardless of the host timezone.
func toBookResponse(b domain.Book) BookResponse {
	const layout = time.RFC3339
	return BookResponse{
		ID:          b.Id(),
		Title:       b.Title(),
		Author:      b.Author(),
		Year:        b.Year(),
		IndexStatus: b.Status().String(),
		Tags:        b.Tags(),
		S3Key:       b.S3Key(),
		CreatedAt:   b.CreatedAt().UTC().Format(layout),
		UpdatedAt:   b.UpdatedAt().UTC().Format(layout),
	}
}

// parsePagination validates limit and offset query parameters.
// Returns safe defaults when the params are absent.
func parsePagination(limitStr, offsetStr string) (limit, offset int, err error) {
	limit = defaultListLimit
	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil || limit <= 0 {
			return 0, 0, errors.New("limit must be a positive integer")
		}
		if limit > maxListLimit {
			limit = maxListLimit
		}
	}
	if offsetStr != "" {
		offset, err = strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

// clamp returns v clamped to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
