package handler_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

type uploadCatalog struct {
	uploadCalls int
	metadata    handler.BookMetadata
}

func (c *uploadCatalog) UploadBook(_ context.Context, metadata handler.BookMetadata, _ handler.CatalogActor, _ string, _ io.Reader) (handler.Book, error) {
	c.uploadCalls++
	c.metadata = metadata
	return handler.Book{ID: "book-id", Year: int(metadata.Year)}, nil
}

func (*uploadCatalog) ListBooks(context.Context, int, string, handler.CatalogActor) (handler.BookPage, error) {
	return handler.BookPage{}, errors.New("unexpected list")
}

func (*uploadCatalog) GetBook(context.Context, string, handler.CatalogActor) (handler.Book, error) {
	return handler.Book{}, errors.New("unexpected get")
}

func (*uploadCatalog) CheckReady(context.Context) error { return nil }

func TestUploadRejectsInvalidPublicationYearsBeforeCallingCatalog(t *testing.T) {
	testCases := []struct {
		name string
		year string
	}{
		{name: "int32 overflow", year: "4294967296"},
		{name: "negative", year: "-1"},
		{name: "too far in future", year: "2147483647"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			catalog := &uploadCatalog{}
			h := handler.NewBooksHandler(catalog)
			req := newUploadRequest(t, testCase.year)
			recorder := httptest.NewRecorder()

			h.Upload(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"invalid_upload"`) {
				t.Fatalf("body = %s, want invalid_upload", recorder.Body.String())
			}
			if catalog.uploadCalls != 0 {
				t.Fatalf("Catalog upload calls = %d, want 0", catalog.uploadCalls)
			}
		})
	}
}

func TestUploadAcceptsMaximumPublicationYearUnchanged(t *testing.T) {
	maximumYear := time.Now().UTC().Year() + 1
	catalog := &uploadCatalog{}
	h := handler.NewBooksHandler(catalog)
	req := newUploadRequest(t, fmt.Sprintf("%d", maximumYear))
	recorder := httptest.NewRecorder()

	h.Upload(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if catalog.uploadCalls != 1 {
		t.Fatalf("Catalog upload calls = %d, want 1", catalog.uploadCalls)
	}
	if catalog.metadata.Year != int32(maximumYear) {
		t.Fatalf("Catalog year = %d, want %d", catalog.metadata.Year, maximumYear)
	}
}

func newUploadRequest(t *testing.T, year string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		t.Fatalf("create metadata part: %v", err)
	}
	if _, err = fmt.Fprintf(metadataPart, `{"title":"Title","author":"Author","year":%s,"tags":[]}`, year); err != nil {
		t.Fatalf("write metadata part: %v", err)
	}
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="book.pdf"`)
	fileHeader.Set("Content-Type", "application/pdf")
	filePart, err := writer.CreatePart(fileHeader)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err = filePart.Write([]byte("%PDF-1.7")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/books", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

type paginationCatalog struct{}

func (paginationCatalog) UploadBook(context.Context, handler.BookMetadata, handler.CatalogActor, string, io.Reader) (handler.Book, error) {
	return handler.Book{}, errors.New("unexpected upload")
}
func (paginationCatalog) ListBooks(context.Context, int, string, handler.CatalogActor) (handler.BookPage, error) {
	return handler.BookPage{}, handler.ErrInvalidPagination
}
func (paginationCatalog) GetBook(context.Context, string, handler.CatalogActor) (handler.Book, error) {
	return handler.Book{}, errors.New("unexpected get")
}
func (paginationCatalog) CheckReady(context.Context) error { return nil }

func TestListMapsCatalogPaginationErrorToBadRequest(t *testing.T) {
	h := handler.NewBooksHandler(paginationCatalog{})
	req := httptest.NewRequest(http.MethodGet, "/books?page_token=short", nil)
	req = req.WithContext(middleware.WithClaims(req.Context(), auth.Claims{UserID: "reader", Role: auth.RoleReader}))
	recorder := httptest.NewRecorder()
	h.List(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Body.String(); got == "" || !strings.Contains(got, "invalid_pagination") {
		t.Fatalf("body = %s", got)
	}
}

type bookLookupCatalog struct {
	getCalls int
	bookID   string
}

func (*bookLookupCatalog) UploadBook(context.Context, handler.BookMetadata, handler.CatalogActor, string, io.Reader) (handler.Book, error) {
	return handler.Book{}, errors.New("unexpected upload")
}

func (*bookLookupCatalog) ListBooks(context.Context, int, string, handler.CatalogActor) (handler.BookPage, error) {
	return handler.BookPage{}, errors.New("unexpected list")
}

func (c *bookLookupCatalog) GetBook(_ context.Context, bookID string, _ handler.CatalogActor) (handler.Book, error) {
	c.getCalls++
	c.bookID = bookID
	return handler.Book{ID: bookID}, nil
}

func (*bookLookupCatalog) CheckReady(context.Context) error { return nil }

const booksTestRequestID = "0123456789abcdef0123456789abcdef"

func TestGetRejectsInvalidBookIDBeforeCallingCatalog(t *testing.T) {
	tests := []struct {
		name   string
		bookID string
	}{
		{name: "empty", bookID: ""},
		{name: "short", bookID: "short"},
		{name: "oversized", bookID: strings.Repeat("A", 4096)},
		{name: "invalid character", bookID: "AAAAAAAAAAAAAAAAAAAAA!"},
		{name: "non canonical", bookID: "AAAAAAAAAAAAAAAAAAAAAB"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &bookLookupCatalog{}
			h := handler.NewBooksHandler(catalog)
			request := bookGetRequest(test.bookID)
			recorder := httptest.NewRecorder()

			h.Get(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"invalid_book_id"`) || !strings.Contains(recorder.Body.String(), `"request_id":"`+booksTestRequestID+`"`) {
				t.Fatalf("body = %s, want sanitized invalid_book_id with request ID", recorder.Body.String())
			}
			if cacheControl := recorder.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") || !strings.Contains(cacheControl, "private") {
				t.Fatalf("Cache-Control = %q, want private no-store", cacheControl)
			}
			if catalog.getCalls != 0 {
				t.Fatalf("Catalog get calls = %d, want 0", catalog.getCalls)
			}
		})
	}
}

func TestGetForwardsCanonicalBookID(t *testing.T) {
	const bookID = "AAAAAAAAAAAAAAAAAAAAAA"
	catalog := &bookLookupCatalog{}
	h := handler.NewBooksHandler(catalog)
	recorder := httptest.NewRecorder()

	h.Get(recorder, bookGetRequest(bookID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if catalog.getCalls != 1 || catalog.bookID != bookID {
		t.Fatalf("Catalog get = calls %d, ID %q; want one call with %q", catalog.getCalls, catalog.bookID, bookID)
	}
}

func bookGetRequest(bookID string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/books/lookup", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("book_id", bookID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, chimiddleware.RequestIDKey, booksTestRequestID)
	ctx = middleware.WithClaims(ctx, auth.Claims{UserID: "reader", Role: auth.RoleReader})
	return request.WithContext(ctx)
}
