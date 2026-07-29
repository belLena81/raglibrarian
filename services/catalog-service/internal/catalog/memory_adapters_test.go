package catalog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"sort"
	"time"
)

func NewService(repository BookRepository, objects OriginalObjectStore, maxBytes int64) *Service {
	return NewServiceWithOptions(repository, objects, ServiceOptions{MaxBytes: maxBytes})
}

type MemoryRepository struct {
	books map[string]Book
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{books: map[string]Book{}}
}

func (r *MemoryRepository) Create(_ context.Context, book Book, _ ...OutboxEvent) error {
	r.books[book.ID] = book
	return nil
}

func (r *MemoryRepository) Get(_ context.Context, id string) (Book, error) {
	book, found := r.books[id]
	if !found || book.ProcessingStatus == BookStatusDeleted {
		return Book{}, ErrNotFound
	}
	return book, nil
}

func (r *MemoryRepository) List(_ context.Context, size int, token string) ([]Book, string, error) {
	books := make([]Book, 0, len(r.books))
	for _, book := range r.books {
		if book.ProcessingStatus != BookStatusDeleted {
			books = append(books, book)
		}
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
	end := min(start+size, len(books))
	next := ""
	if end < len(books) {
		next = encodeCursor(books[end-1])
	}
	return books[start:end], next, nil
}

func (r *MemoryRepository) ApplyLifecycleCommand(_ context.Context, command LifecycleCommand) (Book, bool, error) {
	book, found := r.books[command.BookID]
	if !found {
		return Book{}, false, ErrNotFound
	}
	if book.DeleteCommandID == command.CommandID {
		return book, false, nil
	}
	if book.ProcessingStatus == BookStatusDeleted {
		return Book{}, false, ErrNotFound
	}
	switch command.Kind {
	case LifecycleCommandReindex:
		if !book.CanReindex() {
			return Book{}, false, ErrInvalidTransition
		}
		book.LifecycleVersion++
		book.ProcessingVersion++
		book.ProcessingStatus = BookStatusReindexing
		book.ProcessingStage = BookStageChunksReady
		book.ProcessingFailureCategory = ""
		book.ProcessingUpdatedAt = command.OccurredAt
		book.DeleteCommandID = command.CommandID
	case LifecycleCommandDelete:
		if err := book.TransitionTo(BookStatusDeleting); err != nil {
			return Book{}, false, err
		}
		book.LifecycleVersion++
		book.ProcessingVersion++
		book.ProcessingUpdatedAt = command.OccurredAt
		book.DeleteCommandID = command.CommandID
	default:
		return Book{}, false, ErrInvalidCommand
	}
	r.books[book.ID] = book
	return book, true, nil
}

func (r *MemoryRepository) MarkOriginalDeleted(
	_ context.Context,
	bookID string,
	commandID string,
	lifecycleVersion int64,
	appliedAt time.Time,
) (Book, error) {
	book, found := r.books[bookID]
	if !found || book.DeleteCommandID != commandID || book.LifecycleVersion != lifecycleVersion {
		return Book{}, ErrProcessingEventConflict
	}
	book.OriginalDeleted = true
	if book.ArtifactsDeleted && book.IndexDeleted {
		book.ProcessingStatus = BookStatusDeleted
		book.ProcessingVersion++
		book.ProcessingUpdatedAt = appliedAt
		scrubBookTombstone(&book)
	}
	r.books[bookID] = book
	return book, nil
}

func (r *MemoryRepository) ApplyLifecycleAck(_ context.Context, ack LifecycleAck, appliedAt time.Time) (Book, bool, error) {
	book, found := r.books[ack.BookID]
	if !found || book.ProcessingStatus != BookStatusDeleting ||
		book.DeleteCommandID != ack.CommandID || book.LifecycleVersion != ack.LifecycleVersion {
		return Book{}, false, ErrProcessingEventConflict
	}
	switch ack.EventType {
	case "ingestion.book.artifacts-deleted.v1":
		if book.ArtifactsDeleted {
			return book, false, nil
		}
		book.ArtifactsDeleted = true
	case "retrieval.book.index-deleted.v1":
		if book.IndexDeleted {
			return book, false, nil
		}
		book.IndexDeleted = true
	default:
		return Book{}, false, ErrInvalidProcessingEvent
	}
	if book.OriginalDeleted && book.ArtifactsDeleted && book.IndexDeleted {
		book.ProcessingStatus = BookStatusDeleted
		book.ProcessingVersion++
		book.ProcessingUpdatedAt = appliedAt
		scrubBookTombstone(&book)
	}
	r.books[book.ID] = book
	return book, true, nil
}

func scrubBookTombstone(book *Book) {
	book.Metadata = BookMetadata{}
	book.ObjectReference = ""
	book.Checksum = [32]byte{}
	book.ByteSize = 0
	book.MediaType = ""
	book.ActorID = ""
	book.ProcessingStage = ""
	book.ProcessingFailureCategory = ""
	book.ManifestReference = ""
	book.ManifestChecksum = [32]byte{}
}

type MemoryObjectStore struct {
	objects map[string][]byte
}

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
	return ObjectReceipt{
		Size:           int64(len(data)),
		ChecksumCRC32C: base64.StdEncoding.EncodeToString(checksumBytes),
	}, nil
}

func (s *MemoryObjectStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func (s *MemoryObjectStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, found := s.objects[key]
	if !found {
		return nil, ErrObjectStorageUnavailable
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}
