package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

func TestStubQueryRepository_Search_ReportsRetrievalUnavailable(t *testing.T) {
	query, err := domain.NewQuery("user-1", "What is a goroutine?")
	if !assert.NoError(t, err) {
		return
	}

	results, err := NewStubQueryRepository().Search(context.Background(), query)

	assert.ErrorIs(t, err, domain.ErrRetrievalUnavailable)
	assert.Nil(t, results)
}
