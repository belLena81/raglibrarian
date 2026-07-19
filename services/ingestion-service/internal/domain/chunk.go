package domain

import (
	"crypto/sha256"
	"errors"
	"strings"
)

var ErrInvalidChunk = errors.New("invalid chunk")

type Chunk struct {
	id            string
	bookID        string
	order         uint64
	text          string
	contentSHA256 [32]byte
	chapter       string
	section       string
	pageStart     uint32
	pageEnd       uint32
	tokenStart    uint64
	tokenEnd      uint64
}

type ChunkInput struct {
	ID, BookID, Text, Chapter, Section string
	Order, TokenStart, TokenEnd        uint64
	PageStart, PageEnd                 uint32
}

func NewChunk(input ChunkInput) (Chunk, error) {
	text := strings.TrimSpace(input.Text)
	if !validIdentifier(input.ID) || !validIdentifier(input.BookID) || text == "" || input.PageStart == 0 || input.PageEnd < input.PageStart || input.TokenEnd <= input.TokenStart || len(input.Chapter) > 512 || len(input.Section) > 512 {
		return Chunk{}, ErrInvalidChunk
	}
	return Chunk{id: input.ID, bookID: input.BookID, order: input.Order, text: text, contentSHA256: sha256.Sum256([]byte(text)), chapter: input.Chapter, section: input.Section, pageStart: input.PageStart, pageEnd: input.PageEnd, tokenStart: input.TokenStart, tokenEnd: input.TokenEnd}, nil
}

func (c Chunk) ID() string              { return c.id }
func (c Chunk) BookID() string          { return c.bookID }
func (c Chunk) Order() uint64           { return c.order }
func (c Chunk) Text() string            { return c.text }
func (c Chunk) ContentSHA256() [32]byte { return c.contentSHA256 }
func (c Chunk) Chapter() string         { return c.chapter }
func (c Chunk) Section() string         { return c.section }
func (c Chunk) PageStart() uint32       { return c.pageStart }
func (c Chunk) PageEnd() uint32         { return c.pageEnd }
func (c Chunk) TokenStart() uint64      { return c.tokenStart }
func (c Chunk) TokenEnd() uint64        { return c.tokenEnd }
