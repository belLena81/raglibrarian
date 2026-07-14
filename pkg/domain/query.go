package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Query represents a user's natural language question against the library.
type Query struct {
	id        string
	userID    string
	question  string
	createdAt time.Time
}

// NewQuery constructs a validated Query.
func NewQuery(userID, question string) (Query, error) {
	if strings.TrimSpace(userID) == "" {
		return Query{}, ErrEmptyUserID
	}
	if err := validateQuestion(question); err != nil {
		return Query{}, err
	}

	return Query{
		id:        uuid.NewString(),
		userID:    userID,
		question:  question,
		createdAt: time.Now().UTC(),
	}, nil
}

// NewQueryFromDB reconstructs a Query from persisted data, skipping validation.
// Only repository implementations should call this.
func NewQueryFromDB(id, userID, question string, createdAt time.Time) Query {
	return Query{
		id:        id,
		userID:    userID,
		question:  question,
		createdAt: createdAt,
	}
}

// ID returns the query's unique identifier.
func (q Query) ID() string {
	return q.id
}

// UserID returns the ID of the user who submitted the query.
func (q Query) UserID() string {
	return q.userID
}

// Question returns the natural language question text.
func (q Query) Question() string {
	return q.question
}

// CreatedAt returns when this query was submitted.
func (q Query) CreatedAt() time.Time {
	return q.createdAt
}
