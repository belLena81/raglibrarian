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
