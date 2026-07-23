// Package bookstatus bridges Catalog status events from a lifecycle-managed
// per-instance RabbitMQ queue to the in-process browser notification hub.
package bookstatus

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

const maxEventBytes = 64 << 10

type Hub interface {
	SetAvailable(bool)
	Publish(handler.BookStatusEvent)
}

// Run consumes only the configured, lifecycle-managed instance queue and
// reconnects with bounded backoff until shutdown.
func Run(ctx context.Context, uri, queue string, hub Hub) {
	if uri == "" || queue == "" || hub == nil {
		panic("bookstatus: consumer dependencies are required")
	}
	backoff := time.Second
	for ctx.Err() == nil {
		_ = consume(ctx, uri, queue, hub)
		hub.SetAvailable(false)
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func consume(ctx context.Context, uri, queue string, hub Hub) error {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := amqp091.DialConfig(uri, amqp091.Config{
		Heartbeat: 10 * time.Second,
		Dial: func(network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = channel.Close() }()
	if err = channel.Qos(20, 0, false); err != nil {
		return err
	}
	declared, err := channel.QueueDeclare(queue, false, true, true, false, amqp091.Table{
		"x-max-length-bytes": int64(64 << 20),
		"x-overflow":         "drop-head",
	})
	if err != nil {
		return err
	}
	if err = channel.QueueBind(declared.Name, "catalog.book.processing-status-changed.v1", "raglibrarian.edge-status.v1", false, nil); err != nil {
		return err
	}
	deliveries, err := channel.Consume(declared.Name, "", false, true, false, false, nil)
	if err != nil {
		return err
	}
	hub.SetAvailable(true)
	closed := connection.NotifyClose(make(chan *amqp091.Error, 1))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return errors.New("book status RabbitMQ connection closed")
		case delivery, open := <-deliveries:
			if !open {
				return errors.New("book status delivery stream closed")
			}
			event, valid := decode(delivery)
			if valid {
				hub.Publish(event)
				_ = delivery.Ack(false)
				continue
			}
			// This queue carries a best-effort hint only; clients reconcile from
			// Catalog. Drop poison events rather than retaining untrusted input.
			_ = delivery.Nack(false, false)
		}
	}
}

func decode(delivery amqp091.Delivery) (handler.BookStatusEvent, bool) {
	if delivery.ContentType != "application/x-protobuf" || delivery.Type != "catalog.book.processing-status-changed.v1" ||
		delivery.MessageId == "" || len(delivery.Body) == 0 || len(delivery.Body) > maxEventBytes {
		return handler.BookStatusEvent{}, false
	}
	var message catalogv1.BookProcessingStatusChangedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(delivery.Body, &message); err != nil || len(message.ProtoReflect().GetUnknown()) != 0 {
		return handler.BookStatusEvent{}, false
	}
	if !validIdentifier(message.EventId) || delivery.MessageId != message.EventId || !validIdentifier(message.BookId) ||
		message.Producer != "catalog-service" || message.SchemaVersion != "v1" || message.ProcessingVersion < 1 || message.LifecycleVersion < 1 ||
		message.UpdatedAt == nil || message.UpdatedAt.CheckValid() != nil || !validState(&message) {
		return handler.BookStatusEvent{}, false
	}
	return handler.BookStatusEvent{
		EventID:                   message.EventId,
		BookID:                    message.BookId,
		ProcessingStatus:          message.ProcessingStatus,
		ProcessingStage:           message.ProcessingStage,
		ProcessingFailureCategory: message.ProcessingFailureCategory,
		ProcessingVersion:         message.ProcessingVersion,
		LifecycleVersion:          message.LifecycleVersion,
		CanReindex:                message.CanReindex,
		UpdatedAt:                 message.UpdatedAt.AsTime(),
	}, true
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validState(message *catalogv1.BookProcessingStatusChangedV1) bool {
	switch message.ProcessingStatus {
	case "pending":
		return message.ProcessingStage == "queued" && message.ProcessingFailureCategory == ""
	case "processing":
		return (message.ProcessingStage == "extracting" || message.ProcessingStage == "chunks_ready") && message.ProcessingFailureCategory == ""
	case "indexed":
		return message.ProcessingStage == "indexed" && message.ProcessingFailureCategory == ""
	case "reindexing":
		return message.ProcessingStage == "chunks_ready" && message.ProcessingFailureCategory == ""
	case "deleting":
		return validLifecycleStage(message.ProcessingStage, message.ProcessingFailureCategory)
	case "deleted":
		return message.ProcessingStage == "" && message.ProcessingFailureCategory == ""
	case "failed":
		return message.ProcessingStage == "failed" && validFailure(message.ProcessingFailureCategory)
	default:
		return false
	}
}

func validLifecycleStage(stage, failure string) bool {
	if stage == "failed" {
		return validFailure(failure)
	}
	return failure == "" && (stage == "queued" || stage == "extracting" || stage == "chunks_ready" || stage == "indexed")
}

func validFailure(category string) bool {
	switch category {
	case "encrypted_document", "extraction_not_permitted", "malformed_document", "unsupported_document", "no_extractable_text",
		"resource_limit_exceeded", "source_integrity_mismatch", "processing_timeout", "dependency_unavailable", "internal_processing_error",
		"manifest_integrity", "incompatible_profile", "embedding_unavailable", "vector_store_unavailable", "indexing_timeout", "internal_indexing_error":
		return true
	default:
		return false
	}
}
