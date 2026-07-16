package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

const maxBookMetadataBytes = 4096

var ErrInvalidBookRequest = errors.New("invalid book request")

type BookMetadata struct {
	Title  string   `json:"title"`
	Author string   `json:"author"`
	Year   int      `json:"year"`
	Tags   []string `json:"tags"`
}
type Book struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Author           string    `json:"author"`
	Year             int       `json:"year"`
	Tags             []string  `json:"tags"`
	ProcessingStatus string    `json:"processing_status"`
	CreatedAt        time.Time `json:"created_at"`
}
type BookPage struct {
	Books         []Book
	NextPageToken string
}
type BookCatalog interface {
	UploadBook(context.Context, BookMetadata, string, string, io.Reader) (Book, error)
	ListBooks(context.Context, int, string) (BookPage, error)
	GetBook(context.Context, string) (Book, error)
	CheckReady(context.Context) error
}
type BooksHandler struct{ catalog BookCatalog }

func NewBooksHandler(catalog BookCatalog) *BooksHandler {
	if dependencyMissing(catalog) {
		panic("handler: book catalog is required")
	}
	return &BooksHandler{catalog: catalog}
}

func (h *BooksHandler) Upload(w http.ResponseWriter, r *http.Request) {
	mediaType, params, err := mimeParse(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	metadataPart, err := reader.NextPart()
	if err != nil || metadataPart.FormName() != "metadata" {
		writeError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	var metadata BookMetadata
	if err = json.NewDecoder(io.LimitReader(metadataPart, maxBookMetadataBytes+1)).Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	filePart, err := reader.NextPart()
	if err != nil || filePart.FormName() != "file" {
		writeError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	principal, _ := middleware.PrincipalFromContext(r.Context())
	book, err := h.catalog.UploadBook(r.Context(), metadata, principal.UserID, chimiddleware.GetReqID(r.Context()), &singleFileReader{part: filePart, reader: reader})
	if err != nil {
		writeError(w, mapBookError(err), "upload unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusCreated, book)
}
func (h *BooksHandler) List(w http.ResponseWriter, r *http.Request) {
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	page, err := h.catalog.ListBooks(r.Context(), size, r.URL.Query().Get("page_token"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "catalog unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusOK, map[string]any{"books": page.Books, "next_page_token": page.NextPageToken})
}
func (h *BooksHandler) Get(w http.ResponseWriter, r *http.Request) {
	book, err := h.catalog.GetBook(r.Context(), chi.URLParam(r, "book_id"))
	if err != nil {
		if errors.Is(err, ErrBookNotFound) {
			writeError(w, http.StatusNotFound, "book not found")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "catalog unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusOK, book)
}

var ErrBookNotFound = errors.New("book not found")

func mapBookError(err error) int {
	if errors.Is(err, ErrInvalidBookRequest) {
		return http.StatusBadRequest
	}
	return http.StatusServiceUnavailable
}
func mimeParse(value string) (string, map[string]string, error) { return mime.ParseMediaType(value) }

// singleFileReader preserves streaming while rejecting a multipart body with a
// third part before Catalog can accept the upload.
type singleFileReader struct {
	part      *multipart.Part
	reader    *multipart.Reader
	validated bool
}

func (r *singleFileReader) Read(target []byte) (int, error) {
	n, err := r.part.Read(target)
	if !errors.Is(err, io.EOF) || r.validated {
		return n, err
	}
	r.validated = true
	next, nextErr := r.reader.NextPart()
	if nextErr != io.EOF || next != nil {
		return n, ErrInvalidBookRequest
	}
	return n, io.EOF
}
