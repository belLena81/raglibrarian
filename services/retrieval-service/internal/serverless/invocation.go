// Package serverless contains provider-neutral, one-message retrieval adapters.
package serverless

import (
	"errors"
)

var ErrInvalidMessage = errors.New("invalid broker message")

const maximumMessageBytes = 256 << 10

const (
	MetadataQueue  = "retrieval.book-uploaded.v1"
	ManifestQueue  = "retrieval.chunks-ready.v1"
	IndexQueue     = "retrieval.index-batch.v1"
	LifecycleQueue = "retrieval.book-lifecycle.v1"
)

type Message struct {
	Queue       string
	ContentType string
	EventType   string
	MessageID   string
	Body        []byte
}

// Validate preserves the portable worker's bounded body and route checks.
func Validate(message Message) error {
	if !valid(message) || (message.Queue != MetadataQueue && message.Queue != ManifestQueue && message.Queue != IndexQueue && message.Queue != LifecycleQueue) {
		return ErrInvalidMessage
	}
	switch message.Queue {
	case MetadataQueue:
		if message.EventType != "catalog.book.uploaded.v1" {
			return ErrInvalidMessage
		}
	case ManifestQueue:
		if message.EventType != "ingestion.book.chunks-ready.v1" {
			return ErrInvalidMessage
		}
	case IndexQueue:
		if message.EventType != "retrieval.index-batch.v1" {
			return ErrInvalidMessage
		}
	case LifecycleQueue:
		if message.EventType != "catalog.book.reindex-requested.v1" && message.EventType != "catalog.book.deletion-requested.v1" {
			return ErrInvalidMessage
		}
	}
	return nil
}

func valid(message Message) bool {
	// The portable worker validates content type and bounded body only; it does
	// not use broker MessageID for retrieval event identity.
	return message.ContentType == "application/x-protobuf" && len(message.Body) > 0 && len(message.Body) <= maximumMessageBytes
}
