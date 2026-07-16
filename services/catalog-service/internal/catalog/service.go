package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
	"time"
)

const (
	ChunkSize       = 64 << 10
	DefaultMaxBytes = 50 << 20
)

var (
	ErrInvalidPDF     = errors.New("invalid PDF")
	ErrUploadTooLarge = errors.New("upload too large")
	ErrUploadCapacity = errors.New("upload capacity exhausted")
	ErrNotFound       = errors.New("book not found")
	ErrInvalidStream  = errors.New("invalid upload stream")
)

// UploadInput carries only trusted actor data and immutable metadata.
type UploadInput struct {
	Metadata BookMetadata
	Actor    Actor
	// ActorID is retained temporarily for direct application tests. gRPC callers
	// must use Actor, which carries the live authorization decision.
	ActorID       string
	CorrelationID string
	Reader        io.Reader
}

type BookRepository interface {
	Create(context.Context, Book, OutboxEvent) error
	List(context.Context, int, string) ([]Book, string, error)
	Get(context.Context, string) (Book, error)
}

type OriginalObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Delete(context.Context, string) error
}

type OutboxEvent struct {
	ID, Type, Payload string
	OccurredAt        time.Time
}

// Service coordinates validation, private object storage and atomic persistence.
type Service struct {
	repository BookRepository
	objects    OriginalObjectStore
	now        func() time.Time
	maxBytes   int64
	uploads    chan struct{}
}

func NewService(repository BookRepository, objects OriginalObjectStore, maxBytes int64) *Service {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Service{repository: repository, objects: objects, now: func() time.Time { return time.Now().UTC() }, maxBytes: maxBytes, uploads: make(chan struct{}, 2)}
}

func (s *Service) UploadBook(ctx context.Context, input UploadInput) (Book, error) {
	if err := ValidateMetadata(input.Metadata); err != nil || (input.Actor.UserID == "" && input.ActorID == "") || input.Reader == nil {
		return Book{}, ErrInvalidMetadata
	}
	if input.Actor.UserID != "" && !input.Actor.CanUpload() {
		return Book{}, ErrUnauthorizedActor
	}
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		return Book{}, ErrUploadCapacity
	}
	key, err := generatedID()
	if err != nil {
		return Book{}, fmt.Errorf("generate object reference: %w", err)
	}
	objectReference := "originals/" + key + ".pdf"
	reader := &boundedPDFReader{reader: input.Reader, remaining: s.maxBytes, hash: sha256.New()}
	if err = s.objects.Put(ctx, objectReference, reader); err != nil {
		_ = s.objects.Delete(context.Background(), objectReference)
		return Book{}, sanitizeUploadError(err)
	}
	if err = reader.finish(); err != nil {
		_ = s.objects.Delete(context.Background(), objectReference)
		return Book{}, err
	}
	now := s.now()
	bookID, err := generatedID()
	if err != nil {
		_ = s.objects.Delete(context.Background(), objectReference)
		return Book{}, fmt.Errorf("generate book ID: %w", err)
	}
	book := Book{ID: bookID, Metadata: input.Metadata, ProcessingStatus: BookStatusPending, CreatedAt: now, ObjectReference: objectReference, Checksum: reader.sum(), ByteSize: reader.size}
	eventID, err := generatedID()
	if err != nil {
		_ = s.objects.Delete(context.Background(), objectReference)
		return Book{}, fmt.Errorf("generate event ID: %w", err)
	}
	event := OutboxEvent{ID: eventID, Type: "catalog.book.uploaded.v1", OccurredAt: now, Payload: eventID}
	if err = s.repository.Create(ctx, book, event); err != nil {
		_ = s.objects.Delete(context.Background(), objectReference)
		return Book{}, errors.New("catalog persistence unavailable")
	}
	return book, nil
}

func (s *Service) ListBooks(ctx context.Context, size int, token string) ([]Book, string, error) {
	if size == 0 {
		size = 25
	}
	if size < 1 || size > 100 || (token != "" && !validCursor(token)) {
		return nil, "", ErrInvalidMetadata
	}
	return s.repository.List(ctx, size, token)
}

func validCursor(token string) bool {
	if len(token) > 512 {
		return false
	}
	_, err := decodeCursor(token)
	return err == nil
}

func (s *Service) GetBook(ctx context.Context, id string) (Book, error) {
	if id == "" {
		return Book{}, ErrNotFound
	}
	return s.repository.Get(ctx, id)
}

type boundedPDFReader struct {
	reader    io.Reader
	remaining int64
	size      int64
	hash      hash.Hash
	prefix    [5]byte
	prefixLen int
}

func (r *boundedPDFReader) Read(p []byte) (int, error) {
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.reader.Read(p)
	if n == 0 {
		return n, err
	}
	r.size += int64(n)
	r.remaining -= int64(n)
	for _, value := range p[:n] {
		if r.prefixLen < len(r.prefix) {
			r.prefix[r.prefixLen] = value
			r.prefixLen++
		}
	}
	_, _ = r.hash.Write(p[:n])
	if r.remaining < 0 {
		return n, ErrUploadTooLarge
	}
	return n, err
}

func (r *boundedPDFReader) finish() error {
	if r.prefixLen < len(r.prefix) || string(r.prefix[:]) != "%PDF-" {
		return ErrInvalidPDF
	}
	return nil
}

func (r *boundedPDFReader) sum() [32]byte {
	var checksum [32]byte
	copy(checksum[:], r.hash.Sum(nil))
	return checksum
}

func sanitizeUploadError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, ErrUploadTooLarge) {
		return ErrUploadTooLarge
	}
	return errors.New("object storage unavailable")
}

func generatedID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// MemoryRepository and MemoryObjectStore are deterministic adapters for local development and tests.
type MemoryRepository struct{ books map[string]Book }

func NewMemoryRepository() *MemoryRepository { return &MemoryRepository{books: map[string]Book{}} }
func (r *MemoryRepository) Create(_ context.Context, book Book, _ OutboxEvent) error {
	r.books[book.ID] = book
	return nil
}
func (r *MemoryRepository) Get(_ context.Context, id string) (Book, error) {
	b, ok := r.books[id]
	if !ok {
		return Book{}, ErrNotFound
	}
	return b, nil
}
func (r *MemoryRepository) List(_ context.Context, size int, token string) ([]Book, string, error) {
	books := make([]Book, 0, len(r.books))
	for _, b := range r.books {
		books = append(books, b)
	}
	sort.Slice(books, func(i, j int) bool {
		if books[i].CreatedAt.Equal(books[j].CreatedAt) {
			return books[i].ID > books[j].ID
		}
		return books[i].CreatedAt.After(books[j].CreatedAt)
	})
	start := 0
	if token != "" {
		cursor, _ := decodeCursor(token)
		for start < len(books) && !beforeCursor(books[start], cursor.CreatedAt, cursor.ID) {
			start++
		}
	}
	end := start + size
	if end > len(books) {
		end = len(books)
	}
	next := ""
	if end < len(books) {
		next = encodeCursor(books[end-1])
	}
	return books[start:end], next, nil
}

type pageCursor struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func encodeCursor(book Book) string {
	raw, _ := json.Marshal(pageCursor{Version: 1, CreatedAt: book.CreatedAt.UTC().Format(time.RFC3339Nano), ID: book.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(token string) (pageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return pageCursor{}, err
	}
	var cursor pageCursor
	if err = json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.ID == "" {
		return pageCursor{}, errors.New("invalid cursor")
	}
	if _, err = time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil {
		return pageCursor{}, err
	}
	return cursor, nil
}

func beforeCursor(book Book, createdAt, id string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return false
	}
	return book.CreatedAt.Before(parsed) || (book.CreatedAt.Equal(parsed) && book.ID < id)
}

type MemoryObjectStore struct{ objects map[string][]byte }

func NewMemoryObjectStore() *MemoryObjectStore {
	return &MemoryObjectStore{objects: map[string][]byte{}}
}
func (s *MemoryObjectStore) Put(_ context.Context, key string, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}
func (s *MemoryObjectStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}
