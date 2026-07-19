package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
)

const (
	ChunkSize       = 64 << 10
	DefaultMaxBytes = 25 << 20
)

var (
	ErrInvalidPDF               = errors.New("invalid PDF")
	ErrInvalidPagination        = errors.New("invalid pagination")
	ErrUploadTooLarge           = errors.New("upload too large")
	ErrUploadCapacity           = errors.New("upload capacity exhausted")
	ErrObjectStorageUnavailable = errors.New("object storage unavailable")
	ErrObjectReceiptMismatch    = errors.New("object storage receipt mismatch")
	ErrNotFound                 = errors.New("book not found")
	ErrInvalidStream            = errors.New("invalid upload stream")
)

// UploadInput carries only trusted actor data and immutable metadata.
type UploadInput struct {
	Metadata      BookMetadata
	Actor         Actor
	CorrelationID string
	Reader        io.Reader
}

type BookRepository interface {
	Create(context.Context, Book, ...OutboxEvent) error
	List(context.Context, int, string) ([]Book, string, error)
	Get(context.Context, string) (Book, error)
}

type OriginalObjectStore interface {
	Put(context.Context, string, io.Reader) (ObjectReceipt, error)
	Delete(context.Context, string) error
}

// ObjectReceipt is the server-confirmed result of storing an original.
type ObjectReceipt struct {
	Size           int64
	ChecksumCRC32C string
}

type OutboxEvent struct {
	ID, Type    string
	AggregateID string
	Sequence    int64
	Payload     []byte
	OccurredAt  time.Time
}

// Service coordinates validation, private object storage and atomic persistence.
type Service struct {
	repository BookRepository
	objects    OriginalObjectStore
	now        func() time.Time
	newID      func() (string, error)
	maxBytes   int64
	uploads    chan struct{}
}

// ServiceOptions supplies bounded runtime dependencies without exposing
// transport or storage implementation details to Catalog's application logic.
type ServiceOptions struct {
	MaxBytes          int64
	UploadConcurrency int
	Clock             func() time.Time
	NewID             func() (string, error)
}

func NewService(repository BookRepository, objects OriginalObjectStore, maxBytes int64) *Service {
	return NewServiceWithOptions(repository, objects, ServiceOptions{MaxBytes: maxBytes})
}

func NewServiceWithOptions(repository BookRepository, objects OriginalObjectStore, options ServiceOptions) *Service {
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.UploadConcurrency <= 0 {
		options.UploadConcurrency = 2
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	if options.NewID == nil {
		options.NewID = generatedID
	}
	return &Service{repository: repository, objects: objects, now: options.Clock, maxBytes: options.MaxBytes, uploads: make(chan struct{}, options.UploadConcurrency), newID: options.NewID}
}

func (s *Service) UploadBook(ctx context.Context, input UploadInput) (Book, error) {
	if err := ValidateMetadata(input.Metadata); err != nil || input.Actor.UserID == "" || input.Reader == nil {
		return Book{}, ErrInvalidMetadata
	}
	// Protobuf represents an empty repeated field as nil. Catalog owns the
	// durable representation, where tags is always an empty-or-populated array.
	if input.Metadata.Tags == nil {
		input.Metadata.Tags = []string{}
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
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(input.Reader, prefix); err != nil || string(prefix) != "%PDF-" {
		return Book{}, ErrInvalidPDF
	}
	key, err := s.newID()
	if err != nil {
		return Book{}, fmt.Errorf("generate object reference: %w", err)
	}
	objectReference := "originals/" + key + ".pdf"
	reader := &boundedPDFReader{reader: io.MultiReader(bytes.NewReader(prefix), input.Reader), remaining: s.maxBytes, hash: sha256.New()}
	receipt, err := s.objects.Put(ctx, objectReference, reader)
	if err != nil {
		s.deleteObject(objectReference)
		return Book{}, sanitizeUploadError(err)
	}
	if err = reader.finish(); err != nil {
		s.deleteObject(objectReference)
		return Book{}, err
	}
	if receipt.Size != reader.size || receipt.ChecksumCRC32C == "" {
		s.deleteObject(objectReference)
		return Book{}, ErrObjectReceiptMismatch
	}
	now := s.now()
	bookID, err := s.newID()
	if err != nil {
		s.deleteObject(objectReference)
		return Book{}, fmt.Errorf("generate book ID: %w", err)
	}
	book := Book{
		ID: bookID, Metadata: input.Metadata, ProcessingStatus: BookStatusPending,
		ProcessingStage: BookStageQueued, ProcessingUpdatedAt: now, ProcessingVersion: 1,
		CreatedAt: now, ObjectReference: objectReference, Checksum: reader.sum(), ByteSize: reader.size,
		ActorID: input.Actor.UserID,
	}
	eventID, err := s.newID()
	if err != nil {
		s.deleteObject(objectReference)
		return Book{}, fmt.Errorf("generate event ID: %w", err)
	}
	payload, err := proto.Marshal(&catalogv1.BookUploadedV1{
		EventId: eventID, BookId: book.ID, Title: book.Metadata.Title, Author: book.Metadata.Author,
		Year:            int32(book.Metadata.Year), // #nosec G115 -- ValidateMetadata bounds valid years to int32.
		Tags:            append([]string(nil), book.Metadata.Tags...),
		ObjectReference: book.ObjectReference, Sha256: book.Checksum[:], ByteSize: book.ByteSize,
		MediaType: "application/pdf", ActorId: input.Actor.UserID, CorrelationId: input.CorrelationID,
		OccurredAt: timestamppb.New(now), CausationId: input.CorrelationID, Producer: "catalog-service",
		SchemaVersion: "v1", IdempotencyKey: book.ID,
	})
	if err != nil {
		s.deleteObject(objectReference)
		return Book{}, errors.New("catalog event unavailable")
	}
	event := OutboxEvent{ID: eventID, Type: "catalog.book.uploaded.v1", AggregateID: book.ID, Sequence: 0, OccurredAt: now, Payload: payload}
	statusEventID, err := s.newID()
	if err != nil {
		s.deleteObject(objectReference)
		return Book{}, fmt.Errorf("generate status event ID: %w", err)
	}
	statusPayload, err := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
		EventId: statusEventID, BookId: book.ID, ProcessingStatus: string(book.ProcessingStatus),
		ProcessingStage: string(book.ProcessingStage), ProcessingVersion: book.ProcessingVersion,
		UpdatedAt: timestamppb.New(now), CorrelationId: input.CorrelationID, OccurredAt: timestamppb.New(now),
		CausationId: eventID, Producer: "catalog-service", SchemaVersion: "v1",
		IdempotencyKey: fmt.Sprintf("%s:processing:%d", book.ID, book.ProcessingVersion),
	})
	if err != nil {
		s.deleteObject(objectReference)
		return Book{}, errors.New("catalog status event unavailable")
	}
	statusEvent := OutboxEvent{ID: statusEventID, Type: "catalog.book.processing-status-changed.v1", AggregateID: book.ID, Sequence: book.ProcessingVersion, OccurredAt: now, Payload: statusPayload}
	if err = s.repository.Create(ctx, book, event, statusEvent); err != nil {
		lookupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		persisted, lookupErr := s.repository.Get(lookupCtx, book.ID)
		if lookupErr == nil {
			return persisted, nil
		}
		if errors.Is(lookupErr, ErrNotFound) {
			s.deleteObject(objectReference)
		}
		return Book{}, errors.New("catalog persistence unavailable")
	}
	return book, nil
}

func (s *Service) deleteObject(reference string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.objects.Delete(ctx, reference)
}

func (s *Service) ListBooks(ctx context.Context, size int, token string) ([]Book, string, error) {
	if size == 0 {
		size = 25
	}
	if size < 1 || size > 100 || (token != "" && !validCursor(token)) {
		return nil, "", ErrInvalidPagination
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
	if errors.Is(err, ErrObjectReceiptMismatch) {
		return ErrObjectReceiptMismatch
	}
	return ErrObjectStorageUnavailable
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
func (r *MemoryRepository) Create(_ context.Context, book Book, _ ...OutboxEvent) error {
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
func (s *MemoryObjectStore) Put(_ context.Context, key string, reader io.Reader) (ObjectReceipt, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return ObjectReceipt{}, err
	}
	s.objects[key] = data
	checksum := crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
	checksumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(checksumBytes, checksum)
	return ObjectReceipt{Size: int64(len(data)), ChecksumCRC32C: base64.StdEncoding.EncodeToString(checksumBytes)}, nil
}
func (s *MemoryObjectStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}
