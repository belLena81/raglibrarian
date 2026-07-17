package identitygrpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

const operationTimeout = 5 * time.Second

// VerificationUseCase defines registration and email-verification operations.
type VerificationUseCase interface {
	Register(context.Context, string, string, string, domain.Role) error
	Verify(context.Context, string) (domain.User, error)
	Resend(context.Context, string) error
}

// SessionUseCase defines authentication-session operations exposed over gRPC.
type SessionUseCase interface {
	Login(context.Context, string, string, ...string) (usecase.AuthResult, error)
	Refresh(context.Context, string) (usecase.AuthResult, error)
	ValidateSession(context.Context, string, string) error
	Logout(context.Context, string) error
}
type PasswordResetUseCase interface {
	Request(context.Context, string) error
	Verify(context.Context, string, string) (string, []domain.Role, error)
	Complete(context.Context, string, string, string) error
}

// PrincipalSessionUseCase resolves a session against current account state.
type PrincipalSessionUseCase interface {
	ValidatePrincipal(context.Context, string, string) (domain.Principal, error)
}

// BootstrapUseCase defines the one-time administrator setup workflow.
type BootstrapUseCase interface {
	Required(context.Context) (bool, error)
	Create(context.Context, string, string, string, string) error
}

// ApprovalUseCase defines administrator review of pending librarians.
type ApprovalUseCase interface {
	List(context.Context, domain.Principal, int, *port.PendingCursor) (port.PendingPage, error)
	Approve(context.Context, domain.Principal, string) error
	Reject(context.Context, domain.Principal, string) error
}

// Server adapts Identity application use cases to the generated gRPC contract.
type Server struct {
	identityv1.UnimplementedIdentityServiceServer
	verification  VerificationUseCase
	sessions      SessionUseCase
	passwordReset PasswordResetUseCase
	bootstrap     BootstrapUseCase
	approval      ApprovalUseCase
	notifications port.PendingNotifications
}

// NewServer constructs the complete Identity gRPC adapter.
func NewServer(verification VerificationUseCase, sessions SessionUseCase, passwordReset PasswordResetUseCase, bootstrap BootstrapUseCase, approval ApprovalUseCase, notifications port.PendingNotifications) *Server {
	if verification == nil || sessions == nil || passwordReset == nil || bootstrap == nil || approval == nil || notifications == nil {
		panic("grpc: identity use cases are required")
	}
	return &Server{verification: verification, sessions: sessions, passwordReset: passwordReset, bootstrap: bootstrap, approval: approval, notifications: notifications}
}

// Register accepts a new account registration without disclosing whether its
// email address already exists.
func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid registration")
	}
	if req.Name == "" || req.Role == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid registration")
	}
	if err = s.verification.Register(ctx, req.Name, req.Email, req.Password, domain.Role(req.Role)); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.RegisterResponse{Accepted: true}, nil
}

// VerifyEmail consumes a single-use registration verification token.
func (s *Server) VerifyEmail(ctx context.Context, req *identityv1.VerifyEmailRequest) (*identityv1.VerifyEmailResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if s.verification == nil || req == nil || req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid verification")
	}
	if _, err = s.verification.Verify(ctx, req.Token); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.VerifyEmailResponse{}, nil
}

// ResendVerification rotates verification material while preserving a generic
// response for unknown email addresses.
func (s *Server) ResendVerification(ctx context.Context, req *identityv1.ResendVerificationRequest) (*identityv1.ResendVerificationResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if s.verification == nil || req == nil || req.Email == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid verification request")
	}
	if err = s.verification.Resend(ctx, req.Email); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.ResendVerificationResponse{Accepted: true}, nil
}

// Login authenticates an active, verified account and creates a session.
func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid credentials")
	}
	result, err := s.sessions.Login(ctx, req.Email, req.Password, req.Role)
	if err != nil {
		return nil, toStatus(err)
	}
	response := &identityv1.LoginResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}
	for _, role := range result.AvailableRoles {
		response.AvailableRoles = append(response.AvailableRoles, string(role))
	}
	return response, nil
}

func (s *Server) RequestPasswordReset(ctx context.Context, req *identityv1.PasswordResetRequest) (*identityv1.PasswordResetRequestResponse, error) {
	ctx, cancel, _ := authenticatedOperation(ctx)
	defer cancel()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if err := s.passwordReset.Request(ctx, req.Email); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.PasswordResetRequestResponse{Accepted: true}, nil
}

func (s *Server) VerifyPasswordReset(ctx context.Context, req *identityv1.PasswordResetVerifyRequest) (*identityv1.PasswordResetVerifyResponse, error) {
	ctx, cancel, _ := authenticatedOperation(ctx)
	defer cancel()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	grant, roles, err := s.passwordReset.Verify(ctx, req.Email, req.Code)
	if err != nil {
		return nil, toStatus(err)
	}
	response := &identityv1.PasswordResetVerifyResponse{ResetGrant: grant}
	for _, role := range roles {
		response.AvailableRoles = append(response.AvailableRoles, string(role))
	}
	return response, nil
}

func (s *Server) CompletePasswordReset(ctx context.Context, req *identityv1.PasswordResetCompleteRequest) (*identityv1.PasswordResetCompleteResponse, error) {
	ctx, cancel, _ := authenticatedOperation(ctx)
	defer cancel()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if err := s.passwordReset.Complete(ctx, req.ResetGrant, req.Role, req.Password); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.PasswordResetCompleteResponse{}, nil
}

// Refresh rotates a refresh token and issues fresh session credentials.
func (s *Server) Refresh(ctx context.Context, req *identityv1.RefreshRequest) (*identityv1.RefreshResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.RefreshToken == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid refresh request")
	}
	result, err := s.sessions.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.RefreshResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}, nil
}

// ValidateSession validates session state and, when supported, returns the
// account's current authorization principal.
func (s *Server) ValidateSession(ctx context.Context, req *identityv1.ValidateSessionRequest) (*identityv1.ValidateSessionResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.UserId == "" || req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid session")
	}
	principalSessions, ok := s.sessions.(PrincipalSessionUseCase)
	if !ok {
		if err = s.sessions.ValidateSession(ctx, req.UserId, req.SessionId); err != nil {
			return nil, toStatus(err)
		}
		return &identityv1.ValidateSessionResponse{}, nil
	}
	principal, err := principalSessions.ValidatePrincipal(ctx, req.UserId, req.SessionId)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.ValidateSessionResponse{Principal: principalResponse(principal)}, nil
}

// Logout revokes the requested session.
func (s *Server) Logout(ctx context.Context, req *identityv1.LogoutRequest) (*identityv1.LogoutResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid session")
	}
	if err = s.sessions.Logout(ctx, req.SessionId); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.LogoutResponse{}, nil
}

// GetSetupStatus reports whether the one-time administrator bootstrap remains
// available.
func (s *Server) GetSetupStatus(ctx context.Context, _ *identityv1.GetSetupStatusRequest) (*identityv1.GetSetupStatusResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	required, err := s.bootstrap.Required(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.GetSetupStatusResponse{Required: required}, nil
}

// BootstrapAdmin creates the first administrator after verifier validation.
func (s *Server) BootstrapAdmin(ctx context.Context, req *identityv1.BootstrapAdminRequest) (*identityv1.BootstrapAdminResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Name == "" || req.Email == "" || req.Password == "" || req.BootstrapCode == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid setup request")
	}
	if err = s.bootstrap.Create(ctx, req.Name, req.Email, req.Password, req.BootstrapCode); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.BootstrapAdminResponse{Created: true}, nil
}

// ListPendingLibrarians returns one cursor-paginated page after live
// administrator authorization.
func (s *Server) ListPendingLibrarians(ctx context.Context, req *identityv1.ListPendingLibrariansRequest) (*identityv1.ListPendingLibrariansResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	actor, err := actorFromRequest(req.GetActor())
	if err != nil {
		return nil, err
	}
	cursor, err := decodeCursor(req.GetPageToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid page token")
	}
	page, err := s.approval.List(ctx, actor, int(req.GetPageSize()), cursor)
	if err != nil {
		return nil, toStatus(err)
	}
	response := &identityv1.ListPendingLibrariansResponse{Users: make([]*identityv1.PendingLibrarian, 0, len(page.Users))}
	for _, user := range page.Users {
		response.Users = append(response.Users, &identityv1.PendingLibrarian{
			UserId: user.ID(), Name: user.Name(), Email: user.Email(), RegisteredAt: user.CreatedAt().UTC().Format(time.RFC3339Nano),
		})
	}
	response.NextPageToken = encodeCursor(page.Next)
	return response, nil
}

// ApproveLibrarian activates a pending librarian after live administrator
// authorization.
func (s *Server) ApproveLibrarian(ctx context.Context, req *identityv1.ApproveLibrarianRequest) (*identityv1.ApproveLibrarianResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid decision")
	}
	if err := s.decide(ctx, req.Actor, req.UserId, true); err != nil {
		return nil, err
	}
	return &identityv1.ApproveLibrarianResponse{}, nil
}

// RejectLibrarian records a final rejection after live administrator
// authorization.
func (s *Server) RejectLibrarian(ctx context.Context, req *identityv1.RejectLibrarianRequest) (*identityv1.RejectLibrarianResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid decision")
	}
	if err := s.decide(ctx, req.Actor, req.UserId, false); err != nil {
		return nil, err
	}
	return &identityv1.RejectLibrarianResponse{}, nil
}

func (s *Server) decide(ctx context.Context, actorRequest *identityv1.Actor, userID string, approve bool) error {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if userID == "" {
		return status.Error(codes.InvalidArgument, "invalid decision")
	}
	actor, err := actorFromRequest(actorRequest)
	if err != nil {
		return err
	}
	if approve {
		err = s.approval.Approve(ctx, actor, userID)
	} else {
		err = s.approval.Reject(ctx, actor, userID)
	}
	if err != nil {
		return toStatus(err)
	}
	return nil
}

// WatchPendingLibrarians streams invalidation events for the pending-librarian
// view; clients must refetch authorized state after each event.
func (s *Server) WatchPendingLibrarians(_ *identityv1.WatchPendingLibrariansRequest, stream identityv1.IdentityService_WatchPendingLibrariansServer) error {
	changes, err := s.notifications.Watch(stream.Context())
	if err != nil {
		return status.Error(codes.Unavailable, "notification unavailable")
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case _, ok := <-changes:
			if !ok {
				return status.Error(codes.Unavailable, "notification unavailable")
			}
			if err = stream.Send(&identityv1.WatchPendingLibrariansResponse{Version: 1}); err != nil {
				return err
			}
		}
	}
}

func actorFromRequest(actor *identityv1.Actor) (domain.Principal, error) {
	if actor == nil || actor.UserId == "" || actor.SessionId == "" {
		return domain.Principal{}, status.Error(codes.Unauthenticated, "invalid actor")
	}
	return domain.Principal{UserID: actor.UserId, SessionID: actor.SessionId}, nil
}

type cursorDTO struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
	UserID    string    `json:"user_id"`
}

func encodeCursor(cursor *port.PendingCursor) string {
	if cursor == nil {
		return ""
	}
	encoded, _ := json.Marshal(cursorDTO{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), UserID: cursor.UserID})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(value string) (*port.PendingCursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 512 {
		return nil, domain.ErrConflict
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, domain.ErrConflict
	}
	var dto cursorDTO
	if err = json.Unmarshal(decoded, &dto); err != nil || dto.Version != 1 || dto.UserID == "" || dto.CreatedAt.IsZero() {
		return nil, domain.ErrConflict
	}
	return &port.PendingCursor{CreatedAt: dto.CreatedAt.UTC(), UserID: dto.UserID}, nil
}

func principalResponse(principal domain.Principal) *identityv1.Principal {
	return &identityv1.Principal{
		UserId: principal.UserID, SessionId: principal.SessionID, Name: principal.Name,
		Email: principal.Email, Role: string(principal.Role), Status: string(principal.Status),
	}
}

func authenticatedOperation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	return deadlineCtx, cancel, nil
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "operation canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "operation timed out")
	case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, domain.ErrEmptyEmail),
		errors.Is(err, domain.ErrEmptyName), errors.Is(err, domain.ErrInvalidRole),
		errors.Is(err, domain.ErrInvalidPassword):
		return status.Error(codes.InvalidArgument, "invalid request")
	case errors.Is(err, domain.ErrInvalidBootstrap):
		return status.Error(codes.Unauthenticated, "setup unavailable")
	case errors.Is(err, domain.ErrInvalidVerification):
		return status.Error(codes.InvalidArgument, "invalid verification")
	case errors.Is(err, domain.ErrInvalidPasswordReset):
		return status.Error(codes.InvalidArgument, "invalid password reset")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")
	case errors.Is(err, domain.ErrForbidden):
		return status.Error(codes.PermissionDenied, "forbidden")
	case errors.Is(err, domain.ErrBootstrapComplete), errors.Is(err, domain.ErrConflict):
		return status.Error(codes.FailedPrecondition, "state conflict")
	default:
		return status.Error(codes.Internal, "identity service failure")
	}
}
