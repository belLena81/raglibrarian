package catalogclient

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

func TestMapErrorMapsCatalogAuthorizationFailure(t *testing.T) {
	err := mapError(status.Error(codes.PermissionDenied, "actor is not authorized"))

	if err != handler.ErrBookUnauthorized {
		t.Fatalf("mapError() = %v, want %v", err, handler.ErrBookUnauthorized)
	}
}
