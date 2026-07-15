// Package identityclient adapts Identity's versioned gRPC API to Edge ports.
package identityclient

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
)

const rpcTimeout = 3 * time.Second

// Client is Edge's adapter for the versioned Identity gRPC contract.
type Client struct {
	rpc    identityv1.IdentityServiceClient
	health grpc_health_v1.HealthClient
}

// New constructs a client with mandatory RPC and health dependencies.
func New(rpc identityv1.IdentityServiceClient, health grpc_health_v1.HealthClient) *Client {
	if rpc == nil || health == nil {
		panic("identityclient: rpc and health clients are required")
	}
	return &Client{rpc: rpc, health: health}
}

// CheckReady verifies Identity's standard health service with a bounded deadline.
func (c *Client) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return authflow.ErrUnavailable
	}
	return nil
}

// Register delegates reader registration and maps errors into Edge's taxonomy.
func (c *Client) Register(ctx context.Context, email, password string) (authflow.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.rpc.Register(ctx, &identityv1.RegisterRequest{Email: email, Password: password})
	if err != nil {
		return authflow.Session{}, mapRegisterError(err)
	}
	return registerSession(response), nil
}

// Login delegates credential verification.
func (c *Client) Login(ctx context.Context, email, password string) (authflow.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.rpc.Login(ctx, &identityv1.LoginRequest{Email: email, Password: password})
	if err != nil {
		return authflow.Session{}, mapCredentialError(err)
	}
	return authflow.Session{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		SessionID:    response.SessionId,
		Role:         response.Role,
	}, nil
}

// Refresh rotates an opaque refresh token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (authflow.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.rpc.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: refreshToken})
	if err != nil {
		return authflow.Session{}, mapCredentialError(err)
	}
	return authflow.Session{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		SessionID:    response.SessionId,
		Role:         response.Role,
	}, nil
}

// ValidateSession checks authoritative revocation state.
func (c *Client) ValidateSession(ctx context.Context, userID, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	_, err := c.rpc.ValidateSession(ctx, &identityv1.ValidateSessionRequest{UserId: userID, SessionId: sessionID})
	return mapCredentialError(err)
}

// Logout revokes a verified session.
func (c *Client) Logout(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	_, err := c.rpc.Logout(ctx, &identityv1.LogoutRequest{SessionId: sessionID})
	return mapCredentialError(err)
}

func registerSession(response *identityv1.RegisterResponse) authflow.Session {
	return authflow.Session{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		SessionID:    response.SessionId,
		Role:         response.Role,
	}
}

func mapRegisterError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.AlreadyExists:
		return authflow.ErrEmailTaken
	case codes.InvalidArgument:
		return authflow.ErrInvalidRegistration
	default:
		return authflow.ErrUnavailable
	}
}

func mapCredentialError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.Unauthenticated:
		return authflow.ErrInvalidCredentials
	default:
		return authflow.ErrUnavailable
	}
}
