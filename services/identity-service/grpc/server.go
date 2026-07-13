package identitygrpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

type Server struct { identityv1.UnimplementedIdentityServiceServer; usecase usecase.AuthUseCase }
func NewServer(uc usecase.AuthUseCase) *Server { return &Server{usecase: uc} }

func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	role := domain.Role(req.Role); if role == "" { role = domain.RoleReader }
	token, user, err := s.usecase.Register(ctx, req.Email, req.Password, role)
	if err != nil { return nil, toStatus(err) }
	return &identityv1.RegisterResponse{AccessToken: token, Role: string(user.Role())}, nil
}
func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	token, err := s.usecase.Login(ctx, req.Email, req.Password)
	if err != nil { return nil, toStatus(err) }
	return &identityv1.LoginResponse{AccessToken: token}, nil
}
func toStatus(err error) error {
	switch { case errors.Is(err, domain.ErrEmailTaken): return status.Error(codes.AlreadyExists, "email already registered")
	case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, domain.ErrInvalidRole): return status.Error(codes.InvalidArgument, "invalid registration")
	case errors.Is(err, domain.ErrInvalidCredentials): return status.Error(codes.Unauthenticated, "invalid credentials")
	default: return status.Error(codes.Internal, "identity service failure") }
}
