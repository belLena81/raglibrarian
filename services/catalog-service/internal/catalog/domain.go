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

// BookStatus is intentionally small until an ingestion service owns processing.
type BookStatus string

const BookStatusPending BookStatus = "pending"

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
