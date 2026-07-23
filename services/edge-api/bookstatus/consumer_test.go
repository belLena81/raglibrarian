package bookstatus

import (
	"testing"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
)

func TestDecodeAcceptsSanitizedStatusEvent(t *testing.T) {
	payload, err := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
		EventId: "event-1", BookId: "book-1", ProcessingStatus: "processing", ProcessingStage: "chunks_ready",
		ProcessingVersion: 3, LifecycleVersion: 1, UpdatedAt: timestamppb.New(time.Unix(10, 0)), Producer: "catalog-service", SchemaVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	event, valid := decode(amqp091.Delivery{ContentType: "application/x-protobuf", Type: "catalog.book.processing-status-changed.v1", MessageId: "event-1", Body: payload})
	if !valid || event.BookID != "book-1" || event.ProcessingVersion != 3 {
		t.Fatalf("decoded event = %#v, valid=%v", event, valid)
	}
}

func TestDecodeRejectsSSEIdentifierInjection(t *testing.T) {
	payload, err := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
		EventId: "event-1\nevent: injected", BookId: "book-1", ProcessingStatus: "pending", ProcessingStage: "queued",
		ProcessingVersion: 1, LifecycleVersion: 1, UpdatedAt: timestamppb.Now(), Producer: "catalog-service", SchemaVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, valid := decode(amqp091.Delivery{ContentType: "application/x-protobuf", Type: "catalog.book.processing-status-changed.v1", MessageId: "event-1\nevent: injected", Body: payload}); valid {
		t.Fatal("identifier containing a newline was accepted")
	}
}

func TestValidFailureUsesCanonicalCategories(t *testing.T) {
	if !validFailure("encrypted_document") || !validFailure("vector_store_unavailable") || validFailure("encrypted") {
		t.Fatal("failure category boundary is not canonical")
	}
}

func TestValidStateAcceptsLifecycleProjections(t *testing.T) {
	for _, message := range []*catalogv1.BookProcessingStatusChangedV1{
		{ProcessingStatus: "indexed", ProcessingStage: "indexed"},
		{ProcessingStatus: "reindexing", ProcessingStage: "chunks_ready"},
		{ProcessingStatus: "deleting", ProcessingStage: "indexed"},
		{ProcessingStatus: "deleted"},
	} {
		if !validState(message) {
			t.Fatalf("valid lifecycle projection rejected: %+v", message)
		}
	}
}
