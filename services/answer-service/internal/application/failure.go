package application

import (
	"context"
	"errors"
)

func failureReasonCode(err error, stage string) string {
	if err == nil {
		return "unknown_failure"
	}
	var coded codedReason
	if errors.As(err, &coded) {
		return coded.ReasonCode()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case stage == "validation":
		return "invalid_provider_output"
	case stage == "retrieval":
		return "retrieval_failed"
	case stage == "generator":
		return "provider_failed"
	default:
		return "unknown_failure"
	}
}
