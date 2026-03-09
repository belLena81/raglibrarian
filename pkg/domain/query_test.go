package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

func TestNewQuery_Valid(t *testing.T) {
	query, err := domain.NewQuery("user-id-456", "Which book explains goroutine scheduling?")

	require.NoError(t, err)
	assert.NotEmpty(t, query.Id())
	assert.Equal(t, "user-id-456", query.UserId())
	assert.Equal(t, "Which book explains goroutine scheduling?", query.Question())
	assert.WithinDuration(t, time.Now().UTC(), query.CreatedAt(), time.Second)
}

func TestNewQuery_UniqueIds(t *testing.T) {
	a, err := domain.NewQuery("user-id-456", "Same question?")
	require.NoError(t, err)

	b, err := domain.NewQuery("user-id-456", "Same question?")
	require.NoError(t, err)

	assert.NotEqual(t, a.Id(), b.Id())
}

func TestNewQuery_InvalidUserID(t *testing.T) {
	tests := []struct {
		name   string
		userID string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewQuery(tt.userID, "Valid question?")
			assert.ErrorIs(t, err, domain.ErrEmptyUserId)
		})
	}
}

func TestNewQuery_InvalidQuestion(t *testing.T) {
	tests := []struct {
		name     string
		question string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewQuery("user-id-456", tt.question)
			assert.ErrorIs(t, err, domain.ErrEmptyQuestion)
		})
	}
}
