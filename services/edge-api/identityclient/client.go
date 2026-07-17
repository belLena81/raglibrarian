// Package identityclient adapts Identity's versioned gRPC API to Edge ports.
package identityclient

import (
	"context"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"google.golang.org/grpc/codes"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
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
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	response, err := c.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return authflow.ErrUnavailable
	}
	return nil
}

// Register requests a private, verification-required registration.
func (c *Client) Register(ctx context.Context, name, email, password, role string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.Register(ctx, &identityv1.RegisterRequest{Name: name, Email: email, Password: password, Role: role})
	if err != nil {
		return mapRegisterError(err)
	}
	return nil
}

// VerifyEmail consumes a single-use email-verification token.
func (c *Client) VerifyEmail(ctx context.Context, token string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.VerifyEmail(ctx, &identityv1.VerifyEmailRequest{Token: token})
	if status.Code(err) == codes.InvalidArgument {
		return authflow.ErrInvalidVerification
	}
	return mapDependencyError(err)
}

// ResendVerification requests a new token without revealing account existence.
func (c *Client) ResendVerification(ctx context.Context, email string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.ResendVerification(ctx, &identityv1.ResendVerificationRequest{Email: email})
	return mapDependencyError(err)
}

// Login delegates credential verification.
func (c *Client) Login(ctx context.Context, email, password, role string) (authflow.Session, error) {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	response, err := c.rpc.Login(ctx, &identityv1.LoginRequest{Email: email, Password: password, Role: role})
	if err != nil {
		return authflow.Session{}, mapCredentialError(err)
	}
	return authflow.Session{
		AccessToken:    response.AccessToken,
		RefreshToken:   response.RefreshToken,
		SessionID:      response.SessionId,
		Role:           response.Role,
		AvailableRoles: response.AvailableRoles,
	}, nil
}
func (c *Client) RequestPasswordReset(ctx context.Context, email string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.RequestPasswordReset(ctx, &identityv1.PasswordResetRequest{Email: email})
	return mapDependencyError(err)
}
func (c *Client) VerifyPasswordReset(ctx context.Context, email, code string) (string, []string, error) {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	response, err := c.rpc.VerifyPasswordReset(ctx, &identityv1.PasswordResetVerifyRequest{Email: email, Code: code})
	if err != nil {
		return "", nil, mapPasswordResetError(err)
	}
	return response.ResetGrant, response.AvailableRoles, nil
}
func (c *Client) CompletePasswordReset(ctx context.Context, grant, role, password string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.CompletePasswordReset(ctx, &identityv1.PasswordResetCompleteRequest{ResetGrant: grant, Role: role, Password: password})
	return mapPasswordResetError(err)
}

// Refresh rotates an opaque refresh token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (authflow.Session, error) {
	ctx, cancel := rpcContext(ctx)
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
func (c *Client) ValidateSession(ctx context.Context, userID, sessionID string) (authflow.Principal, error) {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	response, err := c.rpc.ValidateSession(ctx, &identityv1.ValidateSessionRequest{UserId: userID, SessionId: sessionID})
	if err != nil {
		return authflow.Principal{}, mapCredentialError(err)
	}
	principal := response.GetPrincipal()
	if principal == nil || principal.UserId == "" || principal.SessionId == "" {
		return authflow.Principal{}, authflow.ErrInvalidCredentials
	}
	return authflow.Principal{
		UserID: principal.UserId, SessionID: principal.SessionId, Name: principal.Name,
		Email: principal.Email, Role: principal.Role, Status: principal.Status,
	}, nil
}

// Logout revokes a verified session.
func (c *Client) Logout(ctx context.Context, sessionID string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.Logout(ctx, &identityv1.LogoutRequest{SessionId: sessionID})
	return mapCredentialError(err)
}

// SetupStatus reports whether initial administrator bootstrap is required.
func (c *Client) SetupStatus(ctx context.Context) (bool, error) {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	response, err := c.rpc.GetSetupStatus(ctx, &identityv1.GetSetupStatusRequest{})
	if err != nil {
		return false, mapDependencyError(err)
	}
	return response.Required, nil
}

// BootstrapAdmin submits the one-time administrator bootstrap request.
func (c *Client) BootstrapAdmin(ctx context.Context, name, email, password, code string) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	_, err := c.rpc.BootstrapAdmin(ctx, &identityv1.BootstrapAdminRequest{Name: name, Email: email, Password: password, BootstrapCode: code})
	return mapStateError(err)
}

// ListPending returns one bounded page of librarians awaiting review.
func (c *Client) ListPending(ctx context.Context, actor authflow.Principal, pageSize int, pageToken string) (authflow.PendingPage, error) {
	if pageSize < 1 || pageSize > 100 {
		return authflow.PendingPage{}, authflow.ErrInvalidRegistration
	}
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	response, err := c.rpc.ListPendingLibrarians(ctx, &identityv1.ListPendingLibrariansRequest{
		Actor: &identityv1.Actor{UserId: actor.UserID, SessionId: actor.SessionID}, PageSize: int32(pageSize), PageToken: pageToken,
	})
	if err != nil {
		return authflow.PendingPage{}, mapStateError(err)
	}
	page := authflow.PendingPage{NextPageToken: response.NextPageToken, Users: make([]authflow.PendingLibrarian, 0, len(response.Users))}
	for _, user := range response.Users {
		page.Users = append(page.Users, authflow.PendingLibrarian{UserID: user.UserId, Name: user.Name, Email: user.Email, RegisteredAt: user.RegisteredAt})
	}
	return page, nil
}

// Approve requests activation of a pending librarian.
func (c *Client) Approve(ctx context.Context, actor authflow.Principal, userID string) error {
	return c.decide(ctx, actor, userID, true)
}

// Reject requests rejection of a pending librarian.
func (c *Client) Reject(ctx context.Context, actor authflow.Principal, userID string) error {
	return c.decide(ctx, actor, userID, false)
}

func (c *Client) decide(ctx context.Context, actor authflow.Principal, userID string, approve bool) error {
	ctx, cancel := rpcContext(ctx)
	defer cancel()
	var err error
	if approve {
		_, err = c.rpc.ApproveLibrarian(ctx, &identityv1.ApproveLibrarianRequest{Actor: &identityv1.Actor{UserId: actor.UserID, SessionId: actor.SessionID}, UserId: userID})
	} else {
		_, err = c.rpc.RejectLibrarian(ctx, &identityv1.RejectLibrarianRequest{Actor: &identityv1.Actor{UserId: actor.UserID, SessionId: actor.SessionID}, UserId: userID})
	}
	return mapStateError(err)
}

// WatchPending forwards versioned Identity change notifications until failure.
func (c *Client) WatchPending(ctx context.Context, changes chan<- struct{}) error {
	stream, err := c.rpc.WatchPendingLibrarians(ctx, &identityv1.WatchPendingLibrariansRequest{})
	if err != nil {
		return authflow.ErrUnavailable
	}
	for {
		message, recvErr := stream.Recv()
		if recvErr != nil {
			return authflow.ErrUnavailable
		}
		if message.Version != 1 {
			continue
		}
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}

func mapRegisterError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.AlreadyExists:
		return nil
	case codes.InvalidArgument:
		return authflow.ErrInvalidRegistration
	default:
		return authflow.ErrUnavailable
	}
}

func mapPasswordResetError(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.InvalidArgument {
		return authflow.ErrInvalidPasswordReset
	}
	return authflow.ErrUnavailable
}

func mapDependencyError(err error) error {
	if err == nil {
		return nil
	}
	return authflow.ErrUnavailable
}

func mapStateError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.PermissionDenied, codes.Unauthenticated:
		return authflow.ErrForbidden
	case codes.InvalidArgument:
		return authflow.ErrInvalidRegistration
	case codes.FailedPrecondition, codes.AlreadyExists:
		return authflow.ErrConflict
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

func rpcContext(parent context.Context) (context.Context, context.CancelFunc) {
	requestID := chimiddleware.GetReqID(parent)
	if requestID != "" {
		parent = metadata.AppendToOutgoingContext(parent, "x-request-id", requestID)
	}
	return context.WithTimeout(parent, rpcTimeout)
}
