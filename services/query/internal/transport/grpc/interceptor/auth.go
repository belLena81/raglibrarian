// Package interceptor provides gRPC server-side auth for the Query Service.
//
// Two things live here:
//
//  1. AuthServiceServer — the gRPC implementation of auth.v1.AuthService.
//     Metadata and Retrieval services call ValidateToken on this server
//     to verify tokens without holding the PASETO key themselves.
//
//  2. UnaryServerInterceptor — a gRPC interceptor for the Query Service's
//     own inbound gRPC calls (if Query ever exposes gRPC endpoints).
//     It validates the token in the "authorization" metadata header and
//     injects claims into the context.
package interceptor

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authpb "github.com/yourname/raglibrarian/pkg/proto/auth"
	"github.com/yourname/raglibrarian/pkg/tokenverifier"
)

// ── Context key (gRPC) ────────────────────────────────────────────────────────

type grpcCtxKey struct{}

// ClaimsFromCtx retrieves verified claims from a gRPC handler context.
func ClaimsFromCtx(ctx context.Context) *tokenverifier.Claims {
	v, _ := ctx.Value(grpcCtxKey{}).(*tokenverifier.Claims)
	return v
}

// ── AuthServiceServer ─────────────────────────────────────────────────────────

// AuthServiceServer implements the auth.v1.AuthService gRPC server.
// It allows other services to validate tokens without holding the PASETO key.
type AuthServiceServer struct {
	authpb.UnimplementedAuthServiceServer
	verifier tokenverifier.Verifier
}

func NewAuthServiceServer(v tokenverifier.Verifier) *AuthServiceServer {
	return &AuthServiceServer{verifier: v}
}

// ValidateToken decrypts and validates a PASETO token, returning embedded claims.
func (s *AuthServiceServer) ValidateToken(ctx context.Context, req *authpb.ValidateTokenRequest) (*authpb.ValidateTokenResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.Unauthenticated, "token is required")
	}

	claims, err := s.verifier.Verify(ctx, req.Token)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}

	return &authpb.ValidateTokenResponse{
		UserId: claims.UserID.String(),
		Email:  claims.Email,
		Role:   string(claims.Role),
	}, nil
}

// ── UnaryServerInterceptor ────────────────────────────────────────────────────

// UnaryServerInterceptor validates the Bearer token in gRPC metadata for every
// inbound unary RPC on the Query Service. It injects claims into the context.
//
// Usage:
//
//	grpc.NewServer(grpc.UnaryInterceptor(interceptor.UnaryServerInterceptor(verifier)))
func UnaryServerInterceptor(v tokenverifier.Verifier) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		token, err := tokenFromMeta(ctx)
		if err != nil {
			return nil, err
		}

		claims, err := v.Verify(ctx, token)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
		}

		ctx = context.WithValue(ctx, grpcCtxKey{}, claims)
		return handler(ctx, req)
	}
}

// RequireRoleInterceptor wraps a unary interceptor that enforces role membership.
// Chain it after UnaryServerInterceptor.
//
// Usage via grpc.ChainUnaryInterceptor:
//
//	grpc.ChainUnaryInterceptor(
//	    interceptor.UnaryServerInterceptor(verifier),
//	    interceptor.RequireRoleInterceptor(tokenverifier.RoleLibrarian, tokenverifier.RoleAdmin),
//	)
func RequireRoleInterceptor(allowed ...tokenverifier.Role) grpc.UnaryServerInterceptor {
	set := make(map[tokenverifier.Role]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		claims := ClaimsFromCtx(ctx)
		if claims == nil {
			return nil, status.Error(codes.Unauthenticated, "not authenticated")
		}
		if _, ok := set[claims.Role]; !ok {
			return nil, status.Error(codes.PermissionDenied, "insufficient permissions")
		}
		return handler(ctx, req)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func tokenFromMeta(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	// Expect "Bearer <token>" or just the raw token
	raw := vals[0]
	if len(raw) > 7 && raw[:7] == "Bearer " {
		raw = raw[7:]
	}
	if raw == "" {
		return "", status.Error(codes.Unauthenticated, "empty authorization header")
	}
	return raw, nil
}
