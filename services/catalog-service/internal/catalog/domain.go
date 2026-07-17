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

var (
	ErrInvalidMetadata   = errors.New("invalid book metadata")
	ErrUnauthorizedActor = errors.New("unauthorized actor")
	ErrInvalidTransition = errors.New("invalid book status transition")
)

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

// TransitionTo validates and applies a Catalog-owned lifecycle transition.
// Upload is the only Milestone 3 writer and creates pending books; later
// consumers must use the same transition table when reporting processing facts.
func (b *Book) TransitionTo(next BookStatus) error {
	if !validTransition(b.ProcessingStatus, next) {
		return ErrInvalidTransition
	}
	b.ProcessingStatus = next
	return nil
}

func validTransition(current, next BookStatus) bool {
	if current == next {
		return false
	}
	if next == BookStatusDeleting {
		switch current {
		case BookStatusPending, BookStatusProcessing, BookStatusIndexed, BookStatusFailed, BookStatusReindexing:
			return true
		default:
			return false
		}
	}
	switch current {
	case BookStatusPending:
		return next == BookStatusProcessing
	case BookStatusProcessing:
		return next == BookStatusIndexed || next == BookStatusFailed
	case BookStatusIndexed:
		return next == BookStatusReindexing
	case BookStatusReindexing:
		return next == BookStatusIndexed || next == BookStatusFailed
	case BookStatusDeleting:
		return next == BookStatusDeleted
	default:
		return false
	}
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
