package domain

import (
	"errors"
)

// Book errors
var (
	ErrEmptyTitle              = errors.New("book: title must not be empty")
	ErrEmptyAuthor             = errors.New("book: author must not be empty")
	ErrInvalidYear             = errors.New("book: year must be between 1900 and the current year")
	ErrBookNotFound            = errors.New("book: not found")
	ErrDuplicateBook           = errors.New("book: a book with this title, author, and year already exists")
	ErrInvalidStatus           = errors.New("book: unrecognised index status")
	ErrInvalidStatusTransition = errors.New("book: index status transition is not allowed")
	ErrEmptyS3Key              = errors.New("book: s3 key must not be empty")
	ErrInvalidTag              = errors.New("book: tag must not be empty or duplicate")
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

// User errors
var (
	ErrEmptyEmail        = errors.New("user: email must not be empty")
	ErrInvalidEmail      = errors.New("user: email format is invalid")
	ErrEmptyPasswordHash = errors.New("user: password hash must not be empty")
	ErrInvalidRole       = errors.New("user: role must be admin, librarian, or reader")
	ErrEmailTaken        = errors.New("user: email is already registered")
	ErrUserNotFound      = errors.New("user: not found")
	ErrInvalidPassword   = errors.New("user: password is incorrect")
	// ErrInvalidCredentials is returned when a password does not match its hash.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrInvalidToken is returned by Validate for any untrustworthy token.
	ErrInvalidToken = errors.New("auth: token is invalid or expired")
)

// Config errors
var (
	ErrMissingEnvVar    = errors.New("missing environment variable")
	ErrInvalidTokenTTL  = errors.New("invalid token TTL")
	ErrInvalidSecretKey = errors.New("invalid auth secret key")
	ErrInvalidDuration  = errors.New("invalid duration")
)
