package transport

import (
	"bytes"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDecodeUploadedAcceptsFrozenCatalogContract(t *testing.T) {
	message := validUploadMessage()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeUploaded(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.BookID != message.BookId || !bytes.Equal(event.SourceSHA256[:], message.Sha256) {
		t.Fatalf("unexpected event: %#v", event)
	}
	if err = event.Validate(50 << 20); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeUploadedRejectsUnknownWireFields(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	payload = protowire.AppendTag(payload, 99, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	if _, err = DecodeUploaded(payload); err != application.ErrInvalidEvent {
		t.Fatalf("expected invalid event, got %v", err)
	}
}

func validUploadMessage() *catalogv1.BookUploadedV1 {
	return &catalogv1.BookUploadedV1{
		EventId:         "event-1",
		BookId:          "book-1",
		ObjectReference: "originals/01234567-89ab-cdef-0123-456789abcdef.pdf",
		Sha256:          bytes.Repeat([]byte{1}, 32),
		ByteSize:        1024,
		MediaType:       "application/pdf",
		CorrelationId:   "correlation-1",
		OccurredAt:      timestamppb.New(time.Now().UTC()),
		CausationId:     "correlation-1",
		Producer:        "catalog-service",
		SchemaVersion:   "v1",
		IdempotencyKey:  "book-1",
	}
}
