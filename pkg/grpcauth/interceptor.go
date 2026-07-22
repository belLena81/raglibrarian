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

// Policy protects every method of one gRPC service for verified exact peer SANs.
// DNSName remains for callers that authorize one peer; DNSNames is the additive
// allowlist form for services with more than one legitimate caller.
type Policy struct {
	Service  string
	DNSName  string
	DNSNames []string
}

// UnaryServerInterceptor enforces policy while leaving unrelated services, such as health, untouched.
func UnaryServerInterceptor(policy Policy) grpc.UnaryServerInterceptor {
	allowedDNSNames := policy.allowedDNSNames()
	prefix := "/" + policy.Service + "/"
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		if err := authorize(ctx, allowedDNSNames); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor applies the same mTLS SAN policy to streaming RPCs.
func StreamServerInterceptor(policy Policy) grpc.StreamServerInterceptor {
	allowedDNSNames := policy.allowedDNSNames()
	prefix := "/" + policy.Service + "/"
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if strings.HasPrefix(info.FullMethod, prefix) {
			if err := authorize(stream.Context(), allowedDNSNames); err != nil {
				return err
			}
		}
		return handler(srv, stream)
	}
}

func (p Policy) allowedDNSNames() map[string]struct{} {
	if p.Service == "" {
		panic("grpcauth: service is required")
	}
	names := make(map[string]struct{}, len(p.DNSNames)+1)
	if p.DNSName != "" {
		if strings.Contains(p.DNSName, "*") {
			panic("grpcauth: exact DNS names are required")
		}
		names[p.DNSName] = struct{}{}
	}
	for _, name := range p.DNSNames {
		if name == "" || strings.Contains(name, "*") {
			panic("grpcauth: exact DNS names are required")
		}
		names[name] = struct{}{}
	}
	if len(names) == 0 {
		panic("grpcauth: at least one DNS name is required")
	}
	return names
}

func authorize(ctx context.Context, allowedDNSNames map[string]struct{}) error {
	peerInfo, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing peer identity")
	}
	tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return status.Error(codes.Unauthenticated, "missing peer certificate")
	}
	for _, dnsName := range tlsInfo.State.PeerCertificates[0].DNSNames {
		if _, allowed := allowedDNSNames[dnsName]; allowed {
			return nil
		}
	}
	return status.Error(codes.PermissionDenied, "caller is not authorized")
}
