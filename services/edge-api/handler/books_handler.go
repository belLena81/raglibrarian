package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/pkg/logger/safe"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

const maxBookMetadataBytes = 4096

var ErrInvalidBookRequest = errors.New("invalid book request")
var ErrInvalidPagination = errors.New("invalid pagination")

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

// CatalogActor carries the authenticated, live principal to Catalog.
type CatalogActor struct {
	UserID      string
	Role        string
	Status      string
	MaskedEmail string
}
type BookCatalog interface {
	UploadBook(context.Context, BookMetadata, CatalogActor, string, io.Reader) (Book, error)
	ListBooks(context.Context, int, string, CatalogActor) (BookPage, error)
	GetBook(context.Context, string, CatalogActor) (Book, error)
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
		writeBookError(w, r, http.StatusBadRequest, "invalid_upload", "invalid upload")
		return
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	metadataPart, err := reader.NextPart()
	if err != nil || metadataPart.FormName() != "metadata" {
		writeBookError(w, r, http.StatusBadRequest, "invalid_upload", "invalid upload")
		return
	}
	if mediaType, _, parseErr := mime.ParseMediaType(metadataPart.Header.Get("Content-Type")); parseErr != nil || mediaType != "application/json" {
		writeBookError(w, r, http.StatusBadRequest, "invalid_upload", "invalid upload")
		return
	}
	var metadata BookMetadata
	if err = decodeBookMetadata(metadataPart, &metadata); err != nil {
		writeBookError(w, r, http.StatusBadRequest, "invalid_upload", "invalid upload")
		return
	}
	filePart, err := reader.NextPart()
	if err != nil || filePart.FormName() != "file" {
		writeBookError(w, r, http.StatusBadRequest, "invalid_upload", "invalid upload")
		return
	}
	if mediaType, _, parseErr := mime.ParseMediaType(filePart.Header.Get("Content-Type")); parseErr != nil || mediaType != "application/pdf" {
		writeBookError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "unsupported media type")
		return
	}
	principal, _ := middleware.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	actor := catalogActor(principal)
	book, err := h.catalog.UploadBook(ctx, metadata, actor, chimiddleware.GetReqID(r.Context()), &singleFileReader{part: filePart, reader: reader})
	if err != nil {
		status, code, message := mapBookError(err)
		writeBookError(w, r, status, code, message)
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusCreated, book)
}
func (h *BooksHandler) List(w http.ResponseWriter, r *http.Request) {
	sizeValue := r.URL.Query().Get("page_size")
	size := 0
	var err error
	if sizeValue != "" {
		size, err = strconv.Atoi(sizeValue)
		if err != nil || size < 1 || size > 100 {
			writeBookError(w, r, http.StatusBadRequest, "invalid_pagination", "invalid pagination")
			return
		}
	}
	if token := r.URL.Query().Get("page_token"); len(token) > 512 {
		writeBookError(w, r, http.StatusBadRequest, "invalid_pagination", "invalid pagination")
		return
	}
	principal, _ := middleware.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	page, err := h.catalog.ListBooks(ctx, size, r.URL.Query().Get("page_token"), catalogActor(principal))
	if err != nil {
		if errors.Is(err, ErrInvalidPagination) {
			writeBookError(w, r, http.StatusBadRequest, "invalid_pagination", "invalid pagination")
			return
		}
		writeBookError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusOK, map[string]any{"books": page.Books, "next_page_token": page.NextPageToken})
}
func (h *BooksHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, _ := middleware.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	book, err := h.catalog.GetBook(ctx, chi.URLParam(r, "book_id"), catalogActor(principal))
	if err != nil {
		if errors.Is(err, ErrBookNotFound) {
			writeBookError(w, r, http.StatusNotFound, "book_not_found", "book not found")
			return
		}
		writeBookError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusOK, book)
}

var ErrBookNotFound = errors.New("book not found")
var ErrBookUnauthorized = errors.New("book actor is not authorized")
var ErrBookTooLarge = errors.New("book upload too large")
var ErrBookUnsupportedMediaType = errors.New("unsupported book media type")
var ErrBookCapacityExhausted = errors.New("book upload capacity exhausted")

func mapBookError(err error) (int, string, string) {
	switch {
	case errors.Is(err, ErrBookTooLarge):
		return http.StatusRequestEntityTooLarge, "upload_too_large", "upload too large"
	case errors.Is(err, ErrBookUnsupportedMediaType):
		return http.StatusUnsupportedMediaType, "unsupported_media_type", "unsupported media type"
	case errors.Is(err, ErrBookCapacityExhausted):
		return http.StatusTooManyRequests, "upload_capacity_exhausted", "upload capacity exhausted"
	case errors.Is(err, ErrBookUnauthorized):
		return http.StatusForbidden, "book_forbidden", "book forbidden"
	case errors.Is(err, ErrInvalidBookRequest):
		return http.StatusBadRequest, "invalid_upload", "invalid upload"
	default:
		return http.StatusServiceUnavailable, "catalog_unavailable", "catalog unavailable"
	}
}
func mimeParse(value string) (string, map[string]string, error) { return mime.ParseMediaType(value) }

func decodeBookMetadata(reader io.Reader, metadata *BookMetadata) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxBookMetadataBytes+1))
	if err != nil || len(data) > maxBookMetadataBytes {
		return ErrInvalidBookRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(metadata); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidBookRequest
	}
	if strings.TrimSpace(metadata.Title) == "" || strings.TrimSpace(metadata.Author) == "" {
		return ErrInvalidBookRequest
	}
	return nil
}

func writeBookError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, status, struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}{Code: code, Error: message, RequestID: chimiddleware.GetReqID(r.Context())})
}

func catalogActor(principal authflow.Principal) CatalogActor {
	return CatalogActor{
		UserID:      principal.UserID,
		Role:        principal.Role,
		Status:      principal.Status,
		MaskedEmail: safe.MaskedEmail(principal.Email).String(),
	}
}

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
