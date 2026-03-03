// Package tokenverifier defines the shared token verification contract used by
// every service in the monorepo. Services that only need to verify tokens (e.g.
// Metadata, Retrieval) depend on this tiny package instead of importing the
// full PASETO infrastructure from services/query.
//
// The Query Service satisfies this interface via its local PASETO verifier.
// Other services satisfy it by delegating to AuthService over gRPC
// (see services/*/internal/transport/grpc/interceptor).
package tokenverifier

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/proto/auth"
	"github.com/google/uuid"
)

// Role mirrors domain/user.Role but lives in the shared package so
// every service can reference it without importing the query service domain.
type Role string

const (
	RoleReader    Role = "reader"
	RoleLibrarian Role = "librarian"
	RoleAdmin     Role = "admin"
)

func (r Role) IsValid() bool {
	switch r {
	case RoleReader, RoleLibrarian, RoleAdmin:
		return true
	}
	return false
}

// Claims is the decoded, trusted identity embedded in a verified token.
type Claims struct {
	UserID uuid.UUID
	Email  string
	Role   Role
}

// Verifier is the interface every service uses to authenticate inbound requests.
// Two adapters exist:
//   - LocalVerifier  (services/query)  — verifies PASETO directly using the key
//   - GRPCVerifier   (pkg/tokenverifier/grpc.go) — calls AuthService.ValidateToken
type Verifier interface {
	Verify(ctx context.Context, token string) (*Claims, error)
}

// GRPCVerifier delegates token verification to the Query Service via gRPC.
// Metadata and Retrieval services use this so they never hold the PASETO key.
type GRPCVerifier struct {
	client authpb.AuthServiceClient
}

func NewGRPCVerifier(client authpb.AuthServiceClient) *GRPCVerifier {
	return &GRPCVerifier{client: client}
}

func (v *GRPCVerifier) Verify(ctx context.Context, token string) (*Claims, error) {
	resp, err := v.client.ValidateToken(ctx, &authpb.ValidateTokenRequest{Token: token})
	if err != nil {
		return nil, err // gRPC status errors propagate as-is
	}
	id, err := uuid.Parse(resp.UserId)
	if err != nil {
		return nil, err
	}
	return &Claims{
		UserID: id,
		Email:  resp.Email,
		Role:   Role(resp.Role),
	}, nil
}
