package application

import (
	"context"
	"errors"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

type failureError struct {
	category domain.FailureCategory
	err      error
}

func (e failureError) Error() string {
	return e.err.Error()
}

func (e failureError) Unwrap() error {
	return e.err
}

func Failure(category domain.FailureCategory, err error) error {
	if err == nil {
		err = errors.New(string(category))
	}
	return failureError{category: category, err: err}
}

func FailureCategory(err error) domain.FailureCategory {
	if err == nil {
		return domain.FailureInternalIndexing
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.FailureIndexingTimeout
	}
	var typed failureError
	if errors.As(err, &typed) {
		return typed.category
	}
	return domain.FailureInternalIndexing
}

func TerminalIndexingFailure(err error) bool {
	var typed failureError
	if !errors.As(err, &typed) {
		return false
	}
	return typed.category == domain.FailureManifestIntegrity || typed.category == domain.FailureResourceLimit
}
