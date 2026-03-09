package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Query represents a user's natural language question against the library.
type Query struct {
	id        string
	userId    string
	question  string
	createdAt time.Time
}

// NewQuery creates a Query, returning an error if any field is invalid.
func NewQuery(userId, question string) (Query, error) {
	if strings.TrimSpace(userId) == "" {
		return Query{}, ErrEmptyUserId
	}
	if err := validateQuestion(question); err != nil {
		return Query{}, err
	}

	return Query{
		id:        uuid.NewString(),
		userId:    userId,
		question:  question,
		createdAt: time.Now().UTC(),
	}, nil
}

func NewQueryFromDb(id, userId, question string, createdAt time.Time) Query {
	return Query{
		id:        id,
		userId:    userId,
		question:  question,
		createdAt: createdAt,
	}
}

func (q Query) Id() string           { return q.id }
func (q Query) UserId() string       { return q.userId }
func (q Query) Question() string     { return q.question }
func (q Query) CreatedAt() time.Time { return q.createdAt }
