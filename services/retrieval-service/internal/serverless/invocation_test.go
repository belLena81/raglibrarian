package serverless

import (
	"crypto/sha256"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidateAcceptsBoundedMetadataMessage(t *testing.T) {
	if err := Validate(metadataMessage(t)); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidatePreservesWorkerMessageValidation(t *testing.T) {
	message := metadataMessage(t)
	message.MessageID = ""
	if err := Validate(message); err != nil {
		t.Fatalf("Validate() with empty MessageID = %v", err)
	}
	message = metadataMessage(t)
	message.Body = make([]byte, maximumMessageBytes+1)
	if err := Validate(message); err == nil {
		t.Fatal("Validate() accepted oversized body")
	}
}

func TestValidateRejectsInvalidRoutes(t *testing.T) {
	tests := []struct {
		name   string
		update func(Message) Message
	}{
		{name: "queue", update: func(message Message) Message {
			message.Queue = "unexpected"
			return message
		}},
		{name: "content type", update: func(message Message) Message {
			message.ContentType = "text/plain"
			return message
		}},
		{name: "event type", update: func(message Message) Message {
			message.EventType = "unexpected"
			return message
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.update(metadataMessage(t))); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func metadataMessage(t *testing.T) Message {
	t.Helper()
	source := sha256.Sum256([]byte("synthetic source"))
	payload, err := proto.Marshal(&catalogv1.BookUploadedV1{EventId: "event-1", BookId: "book-1", Title: "Book", Author: "Author", Year: 2026, ObjectReference: "books/book-1/source.pdf", Sha256: source[:], ByteSize: 128, MediaType: "application/pdf", ActorId: "actor-1", CorrelationId: "correlation-1", CausationId: "cause-1", Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: "book-1", OccurredAt: timestamppb.New(time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	return Message{Queue: MetadataQueue, ContentType: "application/x-protobuf", EventType: "catalog.book.uploaded.v1", MessageID: "event-1", Body: payload}
}
