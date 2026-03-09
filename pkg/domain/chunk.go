package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Chunk represents a single text excerpt extracted from a Book page.
// It is the unit that gets embedded and stored in the vector database.
type Chunk struct {
	id        string
	bookId    string
	content   string
	pageStart int
	pageEnd   int
	createdAt time.Time
}

// NewChunk creates a Chunk, returning an error if any field is invalid.
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

func (c Chunk) Id() string           { return c.id }
func (c Chunk) BookId() string       { return c.bookId }
func (c Chunk) Content() string      { return c.content }
func (c Chunk) PageStart() int       { return c.pageStart }
func (c Chunk) PageEnd() int         { return c.pageEnd }
func (c Chunk) CreatedAt() time.Time { return c.createdAt }
