package identitygrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestRequireEdgeCallerAuthorizesVerifiedSAN(t *testing.T) {
	ctx := peerContext(&x509.Certificate{DNSNames: []string{"edge-api"}})
	assert.NoError(t, requireEdgeCaller(ctx))
}

func TestRequireEdgeCallerRejectsCatalogSAN(t *testing.T) {
	ctx := peerContext(&x509.Certificate{DNSNames: []string{"catalog-service"}})
	assert.Equal(t, codes.PermissionDenied, status.Code(requireEdgeCaller(ctx)))
}

func TestRequireEdgeCallerRejectsUnknownSANAndIgnoresCommonName(t *testing.T) {
	certificate := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "edge-api"},
		DNSNames: []string{"unknown-client"},
	}
	assert.Equal(t, codes.PermissionDenied, status.Code(requireEdgeCaller(peerContext(certificate))))
}

func peerContext(certificate *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{
		State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}},
	}})
}
