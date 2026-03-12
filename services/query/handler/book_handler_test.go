package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/domain"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
	"github.com/belLena81/raglibrarian/services/query/handler"
)

// ── fakeBookUseCase ───────────────────────────────────────────────────────────
// Per-method injectable error/result for precise test control.

type fakeBookUseCase struct {
	book       domain.Book
	books      []domain.Book
	addErr     error
	getErr     error
	listErr    error
	removeErr  error
	reindexErr error
}

func (f *fakeBookUseCase) AddBook(_ context.Context, title, author string, year int) (domain.Book, error) {
	if f.addErr != nil {
		return domain.Book{}, f.addErr
	}
	if f.book.Id() != "" {
		return f.book, nil
	}
	return domain.NewBook(title, author, year)
}

func (f *fakeBookUseCase) GetBook(_ context.Context, _ string) (domain.Book, error) {
	return f.book, f.getErr
}

func (f *fakeBookUseCase) ListBooks(_ context.Context, _ metarepo.ListFilter) ([]domain.Book, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.books == nil {
		return []domain.Book{}, nil
	}
	return f.books, nil
}

func (f *fakeBookUseCase) RemoveBook(_ context.Context, _ string) error     { return f.removeErr }
func (f *fakeBookUseCase) TriggerReindex(_ context.Context, _ string) error { return f.reindexErr }

// Compile-time: fakeBookUseCase satisfies the unexported bookUseCase interface
// via structural typing — verified by the handler's NewBookHandler accepting it.

// ── helpers ───────────────────────────────────────────────────────────────────

func newBookHandler(t *testing.T, uc *fakeBookUseCase) *handler.BookHandler {
	t.Helper()
	return handler.NewBookHandler(uc, zaptest.NewLogger(t))
}

// withID injects a chi URL parameter named "id" into the request context.
// This is the standard pattern for testing chi handlers without a full router.
func withID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// doPostJSON posts JSON to the handler function and returns the recorder.
func doPostJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func decodeBook(t *testing.T, rr *httptest.ResponseRecorder) handler.BookResponse {
	t.Helper()
	var resp handler.BookResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	return resp
}

func decodeErr(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	return resp.Error
}

// ── Constructor ───────────────────────────────────────────────────────────────

func TestNewBookHandler_NilUseCase_Panics(t *testing.T) {
	assert.Panics(t, func() {
		handler.NewBookHandler(nil, zaptest.NewLogger(t))
	})
}

func TestNewBookHandler_NilLogger_Panics(t *testing.T) {
	assert.Panics(t, func() {
		handler.NewBookHandler(&fakeBookUseCase{}, nil)
	})
}

// ── POST /admin/books — AddBook ───────────────────────────────────────────────

func TestAddBook_Valid_Returns201WithBook(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})

	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{
		Title: "Clean Code", Author: "Robert Martin", Year: 2008,
	})

	require.Equal(t, http.StatusCreated, rr.Code)
	resp := decodeBook(t, rr)
	assert.NotEmpty(t, resp.ID)
	assert.Equal(t, "Clean Code", resp.Title)
	assert.Equal(t, "Robert Martin", resp.Author)
	assert.Equal(t, 2008, resp.Year)
	assert.Equal(t, "pending", resp.IndexStatus)
	assert.NotEmpty(t, resp.CreatedAt)
	assert.NotEmpty(t, resp.UpdatedAt)
}

func TestAddBook_S3Key_OmittedFromResponseWhenEmpty(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "T", Author: "A", Year: 2020})
	require.Equal(t, http.StatusCreated, rr.Code)
	// s3_key must not appear in the JSON when blank — omitempty is load-bearing.
	assert.NotContains(t, rr.Body.String(), "s3_key")
}

func TestAddBook_ResponseHasJSONContentType(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "T", Author: "A", Year: 2020})
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
}

func TestAddBook_MissingTitle_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Author: "Author", Year: 2020})
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert.Contains(t, decodeErr(t, rr), "title")
}

func TestAddBook_MissingAuthor_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "Title", Year: 2020})
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert.Contains(t, decodeErr(t, rr), "author")
}

func TestAddBook_MissingYear_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "Title", Author: "Author"})
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert.Contains(t, decodeErr(t, rr), "year")
}

func TestAddBook_DomainInvalidYear_Returns422(t *testing.T) {
	// Year 1800 fails domain.NewBook validation.
	h := newBookHandler(t, &fakeBookUseCase{})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "Title", Author: "Author", Year: 1800})
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestAddBook_DuplicateBook_Returns409(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{addErr: domain.ErrDuplicateBook})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "T", Author: "A", Year: 2020})
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestAddBook_WrongContentType_Returns415(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodPost, "/",
		bytes.NewBufferString(`{"title":"T","author":"A","year":2020}`))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	h.AddBook(rr, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
}

func TestAddBook_ContentTypeWithCharset_Returns201(t *testing.T) {
	// "application/json; charset=utf-8" must be accepted — only the media type
	// prefix is checked, not the parameters.
	h := newBookHandler(t, &fakeBookUseCase{})
	b, _ := json.Marshal(handler.AddBookRequest{Title: "T", Author: "A", Year: 2020})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rr := httptest.NewRecorder()
	h.AddBook(rr, req)
	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestAddBook_EmptyBody_Returns400(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.AddBook(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAddBook_UnknownJSONField_Returns400(t *testing.T) {
	// DisallowUnknownFields: catches client typos and signals the schema explicitly.
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodPost, "/",
		bytes.NewBufferString(`{"title":"T","author":"A","year":2020,"extra":"field"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.AddBook(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAddBook_InternalError_Returns500_DoesNotLeakDetails(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{addErr: errors.New("db: connection refused at 10.0.0.1:5432")})
	rr := doPostJSON(t, h.AddBook, handler.AddBookRequest{Title: "T", Author: "A", Year: 2020})
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	body := rr.Body.String()
	// Infrastructure details must never be sent to clients.
	assert.NotContains(t, body, "10.0.0.1")
	assert.NotContains(t, body, "connection refused")
	assert.Contains(t, decodeErr(t, rr), "internal server error")
}

// ── GET /admin/books/{id} — GetBook ──────────────────────────────────────────

func TestGetBook_Exists_Returns200(t *testing.T) {
	book, err := domain.NewBook("DDIA", "Kleppmann", 2017)
	require.NoError(t, err)

	h := newBookHandler(t, &fakeBookUseCase{book: book})
	req := withID(httptest.NewRequest(http.MethodGet, "/admin/books/"+book.Id(), nil), book.Id())
	rr := httptest.NewRecorder()
	h.GetBook(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	resp := decodeBook(t, rr)
	assert.Equal(t, book.Id(), resp.ID)
	assert.Equal(t, "DDIA", resp.Title)
	assert.Equal(t, "Kleppmann", resp.Author)
	assert.Equal(t, 2017, resp.Year)
	assert.Equal(t, "pending", resp.IndexStatus)
}

func TestGetBook_NotFound_Returns404(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{getErr: domain.ErrBookNotFound})
	req := withID(httptest.NewRequest(http.MethodGet, "/admin/books/ghost", nil), "ghost")
	rr := httptest.NewRecorder()
	h.GetBook(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Equal(t, "book not found", decodeErr(t, rr))
}

func TestGetBook_EmptyBookID_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{getErr: domain.ErrEmptyBookID})
	req := withID(httptest.NewRequest(http.MethodGet, "/admin/books/", nil), "")
	rr := httptest.NewRecorder()
	h.GetBook(rr, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

// ── GET /admin/books — ListBooks ──────────────────────────────────────────────

func TestListBooks_Empty_Returns200WithEmptySlice(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handler.ListBooksResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.NotNil(t, resp.Books)
	assert.Empty(t, resp.Books)
	assert.Equal(t, 0, resp.Total)
	assert.Equal(t, 50, resp.Limit, "default limit must be 50")
	assert.Equal(t, 0, resp.Offset)
}

func TestListBooks_ReturnsAll(t *testing.T) {
	b1, _ := domain.NewBook("Book A", "Author A", 2020)
	b2, _ := domain.NewBook("Book B", "Author B", 2021)
	h := newBookHandler(t, &fakeBookUseCase{books: []domain.Book{b1, b2}})
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handler.ListBooksResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 2, resp.Total)
	assert.Len(t, resp.Books, 2)
}

func TestListBooks_Pagination_LimitAndOffset(t *testing.T) {
	books := make([]domain.Book, 5)
	for i := range books {
		b, _ := domain.NewBook("Book", "Author", 2000+i)
		books[i] = b
	}
	h := newBookHandler(t, &fakeBookUseCase{books: books})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?limit=2&offset=1", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handler.ListBooksResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 5, resp.Total)
	assert.Len(t, resp.Books, 2)
	assert.Equal(t, 2, resp.Limit)
	assert.Equal(t, 1, resp.Offset)
}

func TestListBooks_OffsetBeyondTotal_ReturnsEmptyPage(t *testing.T) {
	b, _ := domain.NewBook("Book", "Author", 2020)
	h := newBookHandler(t, &fakeBookUseCase{books: []domain.Book{b}})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?offset=999", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handler.ListBooksResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 1, resp.Total)
	assert.Empty(t, resp.Books)
}

func TestListBooks_LimitCappedAt200(t *testing.T) {
	books := make([]domain.Book, 10)
	for i := range books {
		b, _ := domain.NewBook("Book", "Author", 2000+i)
		books[i] = b
	}
	h := newBookHandler(t, &fakeBookUseCase{books: books})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?limit=99999", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handler.ListBooksResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.LessOrEqual(t, resp.Limit, 200)
}

func TestListBooks_InvalidYearFrom_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?year_from=notanint", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestListBooks_InvalidYearTo_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?year_to=notanint", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestListBooks_InvalidIndexStatus_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?index_status=flying", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestListBooks_InvalidLimit_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?limit=-1", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestListBooks_InvalidOffset_Returns422(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/admin/books?offset=-5", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestListBooks_InternalError_Returns500(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{listErr: errors.New("db error")})
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	rr := httptest.NewRecorder()
	h.ListBooks(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.NotContains(t, rr.Body.String(), "db error")
}

// ── DELETE /admin/books/{id} — RemoveBook ─────────────────────────────────────

func TestRemoveBook_Exists_Returns204NoBody(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := withID(httptest.NewRequest(http.MethodDelete, "/admin/books/b-1", nil), "b-1")
	rr := httptest.NewRecorder()
	h.RemoveBook(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Empty(t, rr.Body.String())
}

func TestRemoveBook_NotFound_Returns404(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{removeErr: domain.ErrBookNotFound})
	req := withID(httptest.NewRequest(http.MethodDelete, "/admin/books/ghost", nil), "ghost")
	rr := httptest.NewRecorder()
	h.RemoveBook(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── POST /admin/books/{id}/reindex — TriggerReindex ───────────────────────────

func TestTriggerReindex_Valid_Returns202WithPayload(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{})
	req := withID(httptest.NewRequest(http.MethodPost, "/admin/books/b-1/reindex", nil), "b-1")
	rr := httptest.NewRecorder()
	h.TriggerReindex(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "reindex triggered", resp["message"])
	assert.Equal(t, "b-1", resp["book_id"])
}

func TestTriggerReindex_NotFound_Returns404(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{reindexErr: domain.ErrBookNotFound})
	req := withID(httptest.NewRequest(http.MethodPost, "/admin/books/ghost/reindex", nil), "ghost")
	rr := httptest.NewRecorder()
	h.TriggerReindex(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestTriggerReindex_InvalidTransition_Returns409WithMessage(t *testing.T) {
	// 409 Conflict is the correct code for state-machine violations:
	// the request is valid but the current resource state prevents it.
	h := newBookHandler(t, &fakeBookUseCase{reindexErr: domain.ErrInvalidStatusTransition})
	req := withID(httptest.NewRequest(http.MethodPost, "/admin/books/b-1/reindex", nil), "b-1")
	rr := httptest.NewRecorder()
	h.TriggerReindex(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, decodeErr(t, rr), "state")
}

func TestTriggerReindex_InternalError_Returns500_DoesNotLeak(t *testing.T) {
	h := newBookHandler(t, &fakeBookUseCase{reindexErr: errors.New("broker unreachable at amqp://10.0.0.2")})
	req := withID(httptest.NewRequest(http.MethodPost, "/admin/books/b-1/reindex", nil), "b-1")
	rr := httptest.NewRecorder()
	h.TriggerReindex(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.NotContains(t, rr.Body.String(), "amqp://10.0.0.2")
}
