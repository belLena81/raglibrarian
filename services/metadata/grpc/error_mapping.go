package grpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// domainToStatus converts a domain or use-case error to the appropriate gRPC
// status. All mappings are explicit; unknown errors become Internal so we never
// accidentally leak infrastructure details through a well-typed code.
//
// Mapping rationale:
//   - NotFound          — caller supplied a valid ID that does not exist
//   - InvalidArgument   — caller supplied a structurally bad value (blank ID,
//     unrecognised status string)
//   - FailedPrecondition — the request is valid but the current state forbids
//     it (state machine violation); retrying later may succeed
//   - Internal          — anything else; message is suppressed from the wire
func domainToStatus(err error) error {
	switch {
	case errors.Is(err, domain.ErrBookNotFound):
		return status.Error(codes.NotFound, err.Error())

	case errors.Is(err, domain.ErrEmptyBookID),
		errors.Is(err, domain.ErrInvalidStatus):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, domain.ErrInvalidStatusTransition):
		return status.Error(codes.FailedPrecondition, err.Error())

	default:
		// Do not expose internal error details to gRPC callers.
		return status.Error(codes.Internal, "internal server error")
	}
}
