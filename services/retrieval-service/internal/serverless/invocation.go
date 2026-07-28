// Package serverless contains provider-neutral, one-message retrieval adapters.
package serverless

import (
	"errors"

	"github.com/belLena81/raglibrarian/pkg/contracts"
)

var ErrInvalidMessage = errors.New("invalid broker message")

const maximumMessageBytes = 256 << 10

const (
	MetadataQueue  = contracts.QueueRetrievalMetadata
	ManifestQueue  = contracts.QueueRetrievalManifest
	IndexQueue     = contracts.QueueRetrievalIndex
	LifecycleQueue = contracts.QueueRetrievalLifecycle
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
		if message.EventType != contracts.EventCatalogBookUploaded {
			return ErrInvalidMessage
		}
	case ManifestQueue:
		if message.EventType != contracts.EventIngestionBookChunksReady {
			return ErrInvalidMessage
		}
	case IndexQueue:
		if message.EventType != contracts.EventRetrievalIndexBatch {
			return ErrInvalidMessage
		}
	case LifecycleQueue:
		if message.EventType != contracts.EventCatalogBookReindexRequested && message.EventType != contracts.EventCatalogBookDeletionRequested {
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
