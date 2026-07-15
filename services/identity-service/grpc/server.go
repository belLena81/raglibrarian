package identitygrpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

const operationTimeout = 5 * time.Second

// RegistrationUseCase is the transport-facing registration contract.
type RegistrationUseCase interface {
	Register(context.Context, string, string, domain.Role) (usecase.AuthResult, error)
}

// SessionUseCase is the transport-facing session lifecycle contract.
type SessionUseCase interface {
	Login(context.Context, string, string) (usecase.AuthResult, error)
	Refresh(context.Context, string) (usecase.AuthResult, error)
	ValidateSession(context.Context, string, string) error
	Logout(context.Context, string) error
}

// Server exposes Identity's versioned internal gRPC contract.
type Server struct {
	identityv1.UnimplementedIdentityServiceServer
	registration RegistrationUseCase
	sessions     SessionUseCase
}

// NewServer constructs an Identity gRPC adapter.
func NewServer(registration RegistrationUseCase, sessions SessionUseCase) *Server {
	if registration == nil || sessions == nil {
		panic("grpc: registration and session use cases are required")
	}
	return &Server{registration: registration, sessions: sessions}
}

// Register creates a reader account for an authorized Edge caller.
func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid registration")
	}
	result, err := s.registration.Register(ctx, req.Email, req.Password, domain.RoleReader)
	if err != nil {
		return nil, toStatus(err)
	}
	return registerResponse(result), nil
}

// Login verifies credentials for an authorized Edge caller.
func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid credentials")
	}
	result, err := s.sessions.Login(ctx, req.Email, req.Password)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.LoginResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}, nil
}

// Refresh rotates an Identity refresh token.
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

// ValidateSession confirms server-side session state for verified claims.
func (s *Server) ValidateSession(ctx context.Context, req *identityv1.ValidateSessionRequest) (*identityv1.ValidateSessionResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.UserId == "" || req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid session")
	}
	if err = s.sessions.ValidateSession(ctx, req.UserId, req.SessionId); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.ValidateSessionResponse{}, nil
}

// Logout revokes an Identity session.
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

func registerResponse(result usecase.AuthResult) *identityv1.RegisterResponse {
	return &identityv1.RegisterResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}
}

func authenticatedOperation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}, nil
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	return deadlineCtx, cancel, nil
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "email already registered")
	case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, domain.ErrEmptyEmail), errors.Is(err, domain.ErrInvalidRole), errors.Is(err, domain.ErrInvalidPassword):
		return status.Error(codes.InvalidArgument, "invalid registration")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")
	default:
		return status.Error(codes.Internal, "identity service failure")
	}
}
