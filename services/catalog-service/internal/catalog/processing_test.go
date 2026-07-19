package catalog

import (
	"bytes"
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
)

func TestProcessingServiceValidatesAndAppliesReadyEvent(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	checksum := bytes.Repeat([]byte{1}, 32)
	payload, err := proto.Marshal(&ingestionv1.BookChunksReadyV1{
		EventId: "event-1", BookId: "book-1", SourceSha256: checksum,
		ManifestReference: "books/book-1/manifest.pb", ManifestSha256: checksum, ManifestByteSize: 128,
		PageCount: 2, ChunkCount: 3, CorrelationId: "correlation-1", CausationId: "upload-1",
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: "book-1:v1",
		OccurredAt: timestamppb.New(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &processingRepositoryFake{}
	service := NewProcessingService(repository, func() time.Time { return now.Add(time.Second) }, func() (string, error) { return "status-1", nil })
	changed, err := service.Handle(context.Background(), "ingestion.book.chunks-ready.v1", payload)
	if err != nil || !changed {
		t.Fatalf("Handle() = %v, %v", changed, err)
	}
	if repository.event.BookID != "book-1" || repository.event.Fact.Kind != ProcessingChunksReady || repository.statusEventID != "status-1" {
		t.Fatalf("event = %+v, status ID = %q", repository.event, repository.statusEventID)
	}
}

func TestProcessingServiceRejectsUnknownAndMalformedEvents(t *testing.T) {
	service := NewProcessingService(&processingRepositoryFake{}, time.Now, func() (string, error) { return "id", nil })
	for _, test := range []struct {
		typeName string
		payload  []byte
	}{
		{typeName: "unknown.v1", payload: []byte{1}},
		{typeName: "ingestion.book.processing-started.v1", payload: []byte("not protobuf")},
	} {
		if _, err := service.Handle(context.Background(), test.typeName, test.payload); err != ErrInvalidProcessingEvent {
			t.Fatalf("Handle(%q) error = %v", test.typeName, err)
		}
	}
}

type processingRepositoryFake struct {
	event         ProcessingEvent
	statusEventID string
}

func (r *processingRepositoryFake) ApplyProcessingEvent(_ context.Context, event ProcessingEvent, statusEventID string, _ time.Time) (Book, bool, error) {
	r.event = event
	r.statusEventID = statusEventID
	return Book{}, true, nil
}
