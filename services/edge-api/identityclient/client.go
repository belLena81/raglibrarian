package identityclient

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
)

const rpcTimeout = 3 * time.Second

// ErrUnavailable indicates that Identity could not safely complete an RPC.
// Callers must fail closed rather than treating it as an authentication result.
var ErrUnavailable = errors.New("identity service unavailable")

// Client adapts the versioned Identity gRPC API to the edge handler contract.
type Client struct {
	rpc    identityv1.IdentityServiceClient
	health grpc_health_v1.HealthClient
}

// New constructs a client adapter over the generated Identity client.
func New(rpc identityv1.IdentityServiceClient, health ...grpc_health_v1.HealthClient) *Client {
	client := &Client{rpc: rpc}
	if len(health) > 0 {
		client.health = health[0]
	}
	return client
}

// CheckReady verifies Identity's standard gRPC health service. It never
// returns the underlying transport error to an HTTP caller.
func (c *Client) CheckReady(ctx context.Context) error {
	if c.health == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return ErrUnavailable
	}
	return nil
}

// Register delegates reader registration to Identity.
func (c *Client) Register(ctx context.Context, email, password string) (auth.SessionTokens, domain.User, error) {
	response, err := c.register(ctx, email, password)
	if err != nil {
		return auth.SessionTokens{}, domain.User{}, mapError(err)
	}
	return auth.SessionTokens{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, SessionID: response.SessionId, Role: response.Role}, domain.NewUserFromDB("", email, "", domain.Role(response.Role), time.Time{}), nil
}

// Login delegates credential verification to Identity.
func (c *Client) Login(ctx context.Context, email, password string) (auth.SessionTokens, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.rpc.Login(ctx, &identityv1.LoginRequest{Email: email, Password: password})
	if err != nil {
		return auth.SessionTokens{}, mapError(err)
	}
	return auth.SessionTokens{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, SessionID: response.SessionId, Role: response.Role}, nil
}

// Refresh rotates a browser refresh token and returns a replacement token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (auth.SessionTokens, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	response, err := c.rpc.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: refreshToken})
	if err != nil {
		return auth.SessionTokens{}, mapError(err)
	}
	return auth.SessionTokens{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, SessionID: response.SessionId, Role: response.Role}, nil
}

// ValidateSession checks that the session embedded in a verified access token
// is still active. It is deliberately separate from local PASETO validation.
func (c *Client) ValidateSession(ctx context.Context, userID, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	_, err := c.rpc.ValidateSession(ctx, &identityv1.ValidateSessionRequest{UserId: userID, SessionId: sessionID})
	return mapError(err)
}

// Logout revokes the session associated with the verified access token.
func (c *Client) Logout(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	_, err := c.rpc.Logout(ctx, &identityv1.LogoutRequest{SessionId: sessionID})
	return mapError(err)
}

func (c *Client) register(ctx context.Context, email, password string) (*identityv1.RegisterResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	return c.rpc.Register(ctx, &identityv1.RegisterRequest{Email: email, Password: password})
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.AlreadyExists:
		return domain.ErrEmailTaken
	case codes.InvalidArgument:
		return domain.ErrInvalidEmail
	case codes.Unauthenticated:
		return domain.ErrInvalidCredentials
	case codes.DeadlineExceeded, codes.Unavailable:
		return ErrUnavailable
	default:
		return ErrUnavailable
	}
}
