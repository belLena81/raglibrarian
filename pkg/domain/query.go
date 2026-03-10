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

// NewQuery constructs a validated Query.
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

// NewQueryFromDb reconstructs a Query from persisted data, skipping validation.
// Only repository implementations should call this.
func NewQueryFromDb(id, userId, question string, createdAt time.Time) Query {
	return Query{
		id:        id,
		userId:    userId,
		question:  question,
		createdAt: createdAt,
	}
}

// Id returns the query's unique identifier.
func (q Query) Id() string { return q.id }

// UserId returns the ID of the user who submitted the query.
func (q Query) UserId() string { return q.userId }

// Question returns the natural language question text.
func (q Query) Question() string { return q.question }

// CreatedAt returns when this query was submitted.
func (q Query) CreatedAt() time.Time { return q.createdAt }
