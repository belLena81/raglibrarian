package serverless

import (
	"bytes"
	"testing"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidateAcceptsBoundedUploadMessage(t *testing.T) {
	if err := Validate(uploadMessage(t)); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidateRejectsInvalidMessages(t *testing.T) {
	tests := []struct {
		name   string
		update func(Message) Message
	}{
		{name: "content type", update: func(message Message) Message {
			message.ContentType = "text/plain"
			return message
		}},
		{name: "message id mismatch", update: func(message Message) Message {
			message.MessageID = "other"
			return message
		}},
		{name: "oversized", update: func(message Message) Message {
			message.Body = make([]byte, maximumMessageBytes+1)
			return message
		}},
		{name: "route", update: func(message Message) Message {
			message.EventType = "unexpected"
			return message
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.update(uploadMessage(t))); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func uploadMessage(t *testing.T) Message {
	t.Helper()
	payload, err := proto.Marshal(&catalogv1.BookUploadedV1{EventId: "event-1", BookId: "book-1", ObjectReference: "originals/book-1.pdf", Sha256: bytes.Repeat([]byte{1}, 32), ByteSize: 1, MediaType: "application/pdf", CorrelationId: "correlation-1", CausationId: "cause-1", Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: "book-1", OccurredAt: timestamppb.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return Message{ContentType: "application/x-protobuf", EventType: transport.UploadRoute, MessageID: "event-1", Body: payload}
}
