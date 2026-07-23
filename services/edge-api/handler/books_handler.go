package handler

import (
	"bytes"
	"context"
	"encoding/base64"
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
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	Year      int32    `json:"year"`
	Tags      []string `json:"tags"`
	MediaType string   `json:"-"`
}
type Book struct {
	ID                        string    `json:"id"`
	Title                     string    `json:"title"`
	Author                    string    `json:"author"`
	Year                      int       `json:"year"`
	Tags                      []string  `json:"tags"`
	ProcessingStatus          string    `json:"processing_status"`
	ProcessingStage           string    `json:"processing_stage"`
	ProcessingFailureCategory string    `json:"processing_failure_category,omitempty"`
	ProcessingUpdatedAt       time.Time `json:"processing_updated_at"`
	ProcessingVersion         int64     `json:"processing_version"`
	MediaType                 string    `json:"media_type"`
	LifecycleVersion          int64     `json:"lifecycle_version"`
	CanReindex                bool      `json:"can_reindex"`
	CreatedAt                 time.Time `json:"created_at"`
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
	ReindexBook(context.Context, string, CatalogActor, string, string) (Book, error)
	DeleteBook(context.Context, string, CatalogActor, string, string) (Book, error)
	CheckReady(context.Context) error
}
type BooksHandler struct {
	catalog BookCatalog
	events  *bookEvents
}

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
	fileMediaType, _, parseErr := mime.ParseMediaType(filePart.Header.Get("Content-Type"))
	if parseErr != nil || (fileMediaType != "application/pdf" && fileMediaType != "application/epub+zip") {
		writeBookError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "unsupported media type")
		return
	}
	metadata.MediaType = fileMediaType
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
	bookID := chi.URLParam(r, "book_id")
	if !validBookID(bookID) {
		writeBookError(w, r, http.StatusBadRequest, "invalid_book_id", "invalid book ID")
		return
	}
	principal, _ := middleware.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	book, err := h.catalog.GetBook(ctx, bookID, catalogActor(principal))
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

func (h *BooksHandler) Reindex(w http.ResponseWriter, r *http.Request) {
	h.lifecycleCommand(w, r, h.catalog.ReindexBook)
}

func (h *BooksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	h.lifecycleCommand(w, r, h.catalog.DeleteBook)
}

type lifecycleCommand func(context.Context, string, CatalogActor, string, string) (Book, error)

func (h *BooksHandler) lifecycleCommand(w http.ResponseWriter, r *http.Request, command lifecycleCommand) {
	bookID := chi.URLParam(r, "book_id")
	commandID := r.Header.Get("Idempotency-Key")
	if !validBookID(bookID) {
		writeBookError(w, r, http.StatusBadRequest, "invalid_book_id", "invalid book ID")
		return
	}
	if !validIdempotencyKey(commandID) {
		writeBookError(w, r, http.StatusBadRequest, "invalid_idempotency_key", "invalid idempotency key")
		return
	}
	principal, _ := middleware.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	book, err := command(ctx, bookID, catalogActor(principal), chimiddleware.GetReqID(r.Context()), commandID)
	if err != nil {
		status, code, message := mapBookError(err)
		writeBookError(w, r, status, code, message)
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	writeJSON(w, http.StatusAccepted, book)
}

func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validBookID(value string) bool {
	if len(value) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 16 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

var ErrBookNotFound = errors.New("book not found")
var ErrBookUnauthorized = errors.New("book actor is not authorized")
var ErrBookTooLarge = errors.New("book upload too large")
var ErrBookUnsupportedMediaType = errors.New("unsupported book media type")
var ErrBookCapacityExhausted = errors.New("book upload capacity exhausted")
var ErrBookLifecycleConflict = errors.New("book lifecycle conflict")
var ErrInvalidBookLifecycleRequest = errors.New("invalid book lifecycle request")

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
	case errors.Is(err, ErrBookNotFound):
		return http.StatusNotFound, "book_not_found", "book not found"
	case errors.Is(err, ErrBookLifecycleConflict):
		return http.StatusConflict, "book_lifecycle_conflict", "book lifecycle conflict"
	case errors.Is(err, ErrInvalidBookLifecycleRequest):
		return http.StatusBadRequest, "invalid_lifecycle_command", "invalid lifecycle command"
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
	if metadata.Year < 0 || int64(metadata.Year) > int64(time.Now().UTC().Year()+1) {
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
