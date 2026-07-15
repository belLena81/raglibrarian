// Package internaltls loads mutually authenticated TLS credentials.
package internaltls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"

	"google.golang.org/grpc/credentials"
)

// Files identifies one service's CA, certificate, and private-key files.
type Files struct{ CA, Certificate, Key string }

// ServerCredentials loads TLS 1.3 credentials that require a verified client certificate.
func ServerCredentials(files Files) (credentials.TransportCredentials, error) {
	pool, certificate, err := load(files)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}

// ClientCredentials loads TLS 1.3 credentials and verifies the server DNS SAN.
func ClientCredentials(files Files, serverName string) (credentials.TransportCredentials, error) {
	if serverName == "" {
		return nil, errors.New("internal tls: server name is required")
	}
	pool, certificate, err := load(files)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{certificate},
		ServerName:   serverName,
	}), nil
}

func load(files Files) (*x509.CertPool, tls.Certificate, error) {
	if files.CA == "" || files.Certificate == "" || files.Key == "" {
		return nil, tls.Certificate{}, errors.New("internal tls: CA, certificate, and key files are required")
	}
	ca, err := os.ReadFile(files.CA) // #nosec G703 -- operator-controlled configuration
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, tls.Certificate{}, errors.New("internal tls: invalid CA")
	}
	certificate, err := tls.LoadX509KeyPair(files.Certificate, files.Key)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	return pool, certificate, nil
}
