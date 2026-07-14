package identitygrpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

// Server adapts the Identity application service to the versioned gRPC API.
type Server struct {
	identityv1.UnimplementedIdentityServiceServer
	useCase usecase.AuthUseCase
}

// NewServer constructs a gRPC server backed by the supplied application service.
func NewServer(uc usecase.AuthUseCase) *Server {
	return &Server{useCase: uc}
}

// Register creates a reader account for an authenticated Edge caller.
func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	if err := requireEdgeCaller(ctx); err != nil {
		return nil, err
	}
	token, user, err := s.useCase.Register(ctx, req.Email, req.Password, domain.RoleReader)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.RegisterResponse{AccessToken: token, Role: string(user.Role())}, nil
}

// Login authenticates a user for an authenticated Edge caller.
func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	if err := requireEdgeCaller(ctx); err != nil {
		return nil, err
	}
	token, err := s.useCase.Login(ctx, req.Email, req.Password)
	if err != nil {
		return nil, toStatus(err)
	}
	return &identityv1.LoginResponse{AccessToken: token}, nil
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
	case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, domain.ErrInvalidRole):
		return status.Error(codes.InvalidArgument, "invalid registration")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")
	default:
		return status.Error(codes.Internal, "identity service failure")
	}
}
