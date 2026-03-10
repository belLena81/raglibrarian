package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Chunk is a text excerpt from a Book page, used as the vector embedding unit.
type Chunk struct {
	id        string
	bookId    string
	content   string
	pageStart int
	pageEnd   int
	createdAt time.Time
}

// NewChunk constructs a validated Chunk.
func NewChunk(bookId, content string, pageStart, pageEnd int) (Chunk, error) {
	if strings.TrimSpace(bookId) == "" {
		return Chunk{}, ErrEmptyBookId
	}
	if err := validateContent(content); err != nil {
		return Chunk{}, err
	}
	if err := validatePageRange(pageStart, pageEnd); err != nil {
		return Chunk{}, err
	}

	return Chunk{
		id:        uuid.NewString(),
		bookId:    bookId,
		content:   content,
		pageStart: pageStart,
		pageEnd:   pageEnd,
		createdAt: time.Now().UTC(),
	}, nil
}

// NewChunkFromDB reconstructs a Chunk from persisted data, skipping validation.
// Only repository implementations should call this.
func NewChunkFromDB(id, bookID, content string, pageStart, pageEnd int, createdAt time.Time) Chunk {
	return Chunk{
		id:        id,
		bookId:    bookID,
		content:   content,
		pageStart: pageStart,
		pageEnd:   pageEnd,
		createdAt: createdAt,
	}
}

// Id returns the chunk's unique identifier.
func (c Chunk) Id() string { return c.id }

// BookId returns the ID of the book this chunk belongs to.
func (c Chunk) BookId() string { return c.bookId }

// Content returns the raw text of this chunk.
func (c Chunk) Content() string { return c.content }

// PageStart returns the first page number covered by this chunk.
func (c Chunk) PageStart() int { return c.pageStart }

// PageEnd returns the last page number covered by this chunk.
func (c Chunk) PageEnd() int { return c.pageEnd }

// CreatedAt returns when this chunk was indexed.
func (c Chunk) CreatedAt() time.Time { return c.createdAt }
