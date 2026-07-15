package identitygrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

func TestToStatusPreservesSanitizedContract(t *testing.T) {
	assert.Equal(t, codes.AlreadyExists, status.Code(toStatus(domain.ErrEmailTaken)))
	assert.Equal(t, codes.InvalidArgument, status.Code(toStatus(domain.ErrInvalidPassword)))
	assert.Equal(t, codes.Unauthenticated, status.Code(toStatus(domain.ErrInvalidCredentials)))
}
