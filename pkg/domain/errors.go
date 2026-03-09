package domain

import "errors"

// Book errors
var (
	ErrEmptyTitle  = errors.New("book: title must not be empty")
	ErrEmptyAuthor = errors.New("book: author must not be empty")
	ErrInvalidYear = errors.New("book: year must be between 1900 and the current year")
)

// Chunk errors
var (
	ErrEmptyBookId  = errors.New("chunk: book id must not be empty")
	ErrEmptyContent = errors.New("chunk: content must not be empty")
	ErrInvalidPages = errors.New("chunk: page start must be >= 1 and page end must be >= page start")
)

// Query errors
var (
	ErrEmptyUserId   = errors.New("query: user id must not be empty")
	ErrEmptyQuestion = errors.New("query: question must not be empty")
)

// SearchResult errors
var (
	ErrEmptyQueryId = errors.New("search result: query id must not be empty")
	ErrEmptyChapter = errors.New("search result: chapter must not be empty")
	ErrEmptyPassage = errors.New("search result: passage must not be empty")
	ErrEmptyPages   = errors.New("search result: pages must not be empty")
	ErrInvalidScore = errors.New("search result: score must be between 0 and 1")
)
