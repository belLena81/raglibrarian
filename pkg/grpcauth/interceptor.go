// Package grpcauth authorizes verified internal gRPC peer identities.
package grpcauth

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Policy protects every method of one gRPC service for one verified peer SAN.
type Policy struct{ Service, DNSName string }

// UnaryServerInterceptor enforces policy while leaving unrelated services, such as health, untouched.
func UnaryServerInterceptor(policy Policy) grpc.UnaryServerInterceptor {
	if policy.Service == "" || policy.DNSName == "" {
		panic("grpcauth: service and DNS name are required")
	}
	prefix := "/" + policy.Service + "/"
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		if err := authorize(ctx, policy.DNSName); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor applies the same mTLS SAN policy to streaming RPCs.
func StreamServerInterceptor(policy Policy) grpc.StreamServerInterceptor {
	if policy.Service == "" || policy.DNSName == "" {
		panic("grpcauth: service and DNS name are required")
	}
	prefix := "/" + policy.Service + "/"
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if strings.HasPrefix(info.FullMethod, prefix) {
			if err := authorize(stream.Context(), policy.DNSName); err != nil {
				return err
			}
		}
		return handler(srv, stream)
	}
}

func authorize(ctx context.Context, dnsName string) error {
	peerInfo, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing peer identity")
	}
	tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return status.Error(codes.Unauthenticated, "missing peer certificate")
	}
	if err := tlsInfo.State.PeerCertificates[0].VerifyHostname(dnsName); err != nil {
		return status.Error(codes.PermissionDenied, "caller is not authorized")
	}
	return nil
}
