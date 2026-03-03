// Package interceptor provides the gRPC server-side auth interceptor for
// the Metadata and Retrieval services.
//
// These services never hold the PASETO symmetric key. Instead they delegate
// token validation to the Query Service's AuthService gRPC endpoint via the
// pkg/tokenverifier.GRPCVerifier.
//
// Usage in main.go of metadata/retrieval service:
//
//	authConn, _ := grpc.Dial(querySvcAddr, grpc.WithTransportCredentials(...))
//	verifier := tokenverifier.NewGRPCVerifier(authpb.NewAuthServiceClient(authConn))
//
//	grpcServer := grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(
//	        interceptor.UnaryServerInterceptor(verifier),
//	    ),
//	)
package interceptor

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/yourname/raglibrarian/pkg/tokenverifier"
)

type ctxKey struct{}

// ClaimsFromCtx retrieves verified claims from a gRPC handler context.
func ClaimsFromCtx(ctx context.Context) *tokenverifier.Claims {
	v, _ := ctx.Value(ctxKey{}).(*tokenverifier.Claims)
	return v
}

// UnaryServerInterceptor validates every inbound unary RPC using the provided
// Verifier. For Metadata/Retrieval this will be a GRPCVerifier that calls
// Query Service's AuthService.ValidateToken.
func UnaryServerInterceptor(v tokenverifier.Verifier) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
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
		ctx = context.WithValue(ctx, ctxKey{}, claims)
		return handler(ctx, req)
	}
}

// RequireRoleInterceptor enforces role membership. Chain after UnaryServerInterceptor.
func RequireRoleInterceptor(allowed ...tokenverifier.Role) grpc.UnaryServerInterceptor {
	set := make(map[tokenverifier.Role]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
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

func tokenFromMeta(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	raw := vals[0]
	if len(raw) > 7 && raw[:7] == "Bearer " {
		raw = raw[7:]
	}
	if raw == "" {
		return "", status.Error(codes.Unauthenticated, "empty authorization header")
	}
	return raw, nil
}
