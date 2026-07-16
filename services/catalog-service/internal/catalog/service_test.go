package catalog

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestUploadBookStoresPendingPDF(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewService(repository, objects, 1024)

	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata: BookMetadata{Title: "A title", Author: "An author", Year: 2026, Tags: []string{"go"}},
		ActorID:  "actor-1", Reader: bytes.NewBufferString("%PDF-1.7\nbody"),
	})

	if err != nil {
		t.Fatal(err)
	}
	if book.ProcessingStatus != BookStatusPending {
		t.Fatalf("status = %q", book.ProcessingStatus)
	}
	if len(objects.objects) != 1 {
		t.Fatalf("objects = %d", len(objects.objects))
	}
}

func TestUploadBookRejectsInactiveOrReaderActor(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 1024)
	for _, actor := range []Actor{
		{UserID: "reader", Role: "reader", Status: "active"},
		{UserID: "librarian", Role: "librarian", Status: "pending"},
	} {
		_, err := service.UploadBook(context.Background(), UploadInput{
			Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026},
			Actor:    actor,
			Reader:   bytes.NewBufferString("%PDF-1.7\nbody"),
		})
		if !errors.Is(err, ErrUnauthorizedActor) {
			t.Fatalf("actor %+v error = %v", actor, err)
		}
	}
}

func TestMemoryRepositoryUsesNewestFirstTimestampAndIDCursor(t *testing.T) {
	repository := NewMemoryRepository()
	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "c", "b"} {
		if err := repository.Create(context.Background(), Book{ID: id, CreatedAt: createdAt}, OutboxEvent{}); err != nil {
			t.Fatal(err)
		}
	}
	first, cursor, err := repository.List(context.Background(), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "c" || first[1].ID != "b" || cursor == "" {
		t.Fatalf("first page = %#v, cursor = %q", first, cursor)
	}
	second, next, err := repository.List(context.Background(), 2, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != "a" || next != "" {
		t.Fatalf("second page = %#v, next = %q", second, next)
	}
}

func TestUploadBookRejectsSpoofedPDFAndCompensates(t *testing.T) {
	objects := NewMemoryObjectStore()
	service := NewService(NewMemoryRepository(), objects, 1024)
	_, err := service.UploadBook(context.Background(), UploadInput{Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026}, ActorID: "actor", Reader: bytes.NewBufferString("not a pdf")})
	if !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("error = %v", err)
	}
	if len(objects.objects) != 0 {
		t.Fatalf("objects = %d", len(objects.objects))
	}
}

func TestUploadBookEnforcesSizeLimit(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 5)
	_, err := service.UploadBook(context.Background(), UploadInput{Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026}, ActorID: "actor", Reader: bytes.NewBufferString("%PDF-too-large")})
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("error = %v", err)
	}
}
