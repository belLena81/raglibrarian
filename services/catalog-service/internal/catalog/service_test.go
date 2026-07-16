package catalog

import (
	"bytes"
	"context"
	"errors"
	"testing"
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
