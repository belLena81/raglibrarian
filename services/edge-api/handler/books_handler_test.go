package handler_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

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
