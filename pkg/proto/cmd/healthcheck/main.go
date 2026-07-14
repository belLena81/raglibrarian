// Command healthcheck probes an HTTP endpoint or the standard gRPC health API.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if url := os.Getenv("HEALTHCHECK_HTTP_URL"); url != "" {
		if err := checkHTTP(ctx, url); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := checkGRPC(ctx); err != nil {
		os.Exit(1)
	}
}

func checkHTTP(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- operator-controlled local health endpoint
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("health endpoint is not ready")
	}
	return nil
}

func checkGRPC(ctx context.Context) error {
	creds, err := clientCredentials()
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(os.Getenv("HEALTHCHECK_GRPC_TARGET"), grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	response, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return errors.New("gRPC service is not serving")
	}
	return nil
}

func clientCredentials() (credentials.TransportCredentials, error) {
	ca, err := os.ReadFile(os.Getenv("INTERNAL_TLS_CA_FILE")) // #nosec G703 -- operator-controlled runtime configuration
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid internal CA")
	}
	cert, err := tls.LoadX509KeyPair(os.Getenv("HEALTHCHECK_TLS_CERT_FILE"), os.Getenv("HEALTHCHECK_TLS_KEY_FILE"))
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		ServerName:   os.Getenv("HEALTHCHECK_TLS_SERVER_NAME"),
	}), nil
}
