// Package catalog contains Catalog's transport-independent domain model.
package catalog

import (
	"errors"
	"strings"
	"time"
)

const (
	maxTitleLength  = 256
	maxAuthorLength = 256
	maxTags         = 20
	maxTagLength    = 64
)

var ErrInvalidMetadata = errors.New("invalid book metadata")

var ErrUnauthorizedActor = errors.New("unauthorized actor")

// BookStatus records the Catalog-owned publication lifecycle.
type BookStatus string

const (
	BookStatusPending    BookStatus = "pending"
	BookStatusProcessing BookStatus = "processing"
	BookStatusIndexed    BookStatus = "indexed"
	BookStatusFailed     BookStatus = "failed"
	BookStatusReindexing BookStatus = "reindexing"
	BookStatusDeleting   BookStatus = "deleting"
	BookStatusDeleted    BookStatus = "deleted"
)

// Actor is a live principal forwarded only by the authenticated Edge service.
type Actor struct {
	UserID      string
	Role        string
	Status      string
	MaskedEmail string
}

func (a Actor) CanRead() bool {
	if a.Status != "active" || a.UserID == "" {
		return false
	}
	return a.Role == "reader" || a.Role == "librarian" || a.Role == "admin"
}

func (a Actor) CanUpload() bool {
	return a.Status == "active" && a.UserID != "" && (a.Role == "librarian" || a.Role == "admin")
}

// BookMetadata is immutable upload metadata.
type BookMetadata struct {
	Title  string
	Author string
	Year   int
	Tags   []string
}

// Book is Catalog's aggregate. Storage details never leave this package.
type Book struct {
	ID               string
	Metadata         BookMetadata
	ProcessingStatus BookStatus
	CreatedAt        time.Time
	ObjectReference  string
	Checksum         [32]byte
	ByteSize         int64
	ActorID          string
}

func ValidateMetadata(metadata BookMetadata) error {
	if len(strings.TrimSpace(metadata.Title)) == 0 || len(metadata.Title) > maxTitleLength ||
		len(strings.TrimSpace(metadata.Author)) == 0 || len(metadata.Author) > maxAuthorLength ||
		metadata.Year < 0 || metadata.Year > time.Now().UTC().Year()+1 || len(metadata.Tags) > maxTags {
		return ErrInvalidMetadata
	}
	for _, tag := range metadata.Tags {
		if len(strings.TrimSpace(tag)) == 0 || len(tag) > maxTagLength {
			return ErrInvalidMetadata
		}
	}
	return nil
}
