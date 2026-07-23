// Package serverless adapts exactly one broker message to the ingestion use cases.
package serverless

import (
	"errors"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
)

var ErrInvalidMessage = errors.New("invalid broker message")

const maximumMessageBytes = 256 << 10

// Message is the broker metadata required to validate one AMQP delivery.
// It intentionally contains no transport connection or credential details.
type Message struct {
	ContentType string
	EventType   string
	MessageID   string
	Body        []byte
}

// Validate is the authoritative serverless boundary validation. Processing and
// AMQP settlement remain separate so the worker retry policy stays unchanged.
func Validate(message Message) error {
	if message.ContentType != "application/x-protobuf" || message.MessageID == "" || len(message.Body) == 0 || len(message.Body) > maximumMessageBytes {
		return ErrInvalidMessage
	}
	switch message.EventType {
	case transport.UploadRoute:
		event, err := transport.DecodeUploaded(message.Body)
		if err != nil || event.EventID != message.MessageID {
			return ErrInvalidMessage
		}
	case transport.DeletionRoute:
		event, err := transport.DecodeDeletion(message.Body)
		if err != nil || event.EventID != message.MessageID {
			return ErrInvalidMessage
		}
	default:
		return ErrInvalidMessage
	}
	return nil
}
