package identitygrpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

const operationTimeout = 5 * time.Second

type Server struct {
	identityv1.UnimplementedIdentityServiceServer
	useCase usecase.AuthUseCase
}

func NewServer(uc usecase.AuthUseCase) *Server { return &Server{useCase: uc} }

func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid registration")
	}
	result, _, err := s.useCase.Register(ctx, req.Email, req.Password, domain.RoleReader)
	if err != nil {
		return nil, toStatus(err)
	}
	return registerResponse(result), nil
}

func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid credentials")
	}
	result, err := s.useCase.Login(ctx, req.Email, req.Password)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.LoginResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}, nil
}

func (s *Server) Refresh(ctx context.Context, req *identityv1.RefreshRequest) (*identityv1.RefreshResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.RefreshToken == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid refresh request")
	}
	result, err := s.useCase.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.RefreshResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}, nil
}

func (s *Server) ValidateSession(ctx context.Context, req *identityv1.ValidateSessionRequest) (*identityv1.ValidateSessionResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.UserId == "" || req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid session")
	}
	if err = s.useCase.ValidateSession(ctx, req.UserId, req.SessionId); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.ValidateSessionResponse{}, nil
}

func (s *Server) Logout(ctx context.Context, req *identityv1.LogoutRequest) (*identityv1.LogoutResponse, error) {
	ctx, cancel, err := authenticatedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if req == nil || req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid session")
	}
	if err = s.useCase.Logout(ctx, req.SessionId); err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.LogoutResponse{}, nil
}

func registerResponse(result usecase.AuthResult) *identityv1.RegisterResponse {
	return &identityv1.RegisterResponse{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, SessionId: result.SessionID, Role: string(result.Role)}
}

func authenticatedOperation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := requireEdgeCaller(ctx); err != nil {
		return nil, func() {}, err
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}, nil
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	return deadlineCtx, cancel, nil
}

func requireEdgeCaller(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing peer identity")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return status.Error(codes.Unauthenticated, "missing peer certificate")
	}
	if tlsInfo.State.PeerCertificates[0].Subject.CommonName != "edge-api" {
		return status.Error(codes.PermissionDenied, "caller is not authorized")
	}
	return nil
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "email already registered")
	case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, domain.ErrInvalidRole), errors.Is(err, domain.ErrInvalidPassword):
		return status.Error(codes.InvalidArgument, "invalid registration")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")
	default:
		return status.Error(codes.Internal, "identity service failure")
	}
}
