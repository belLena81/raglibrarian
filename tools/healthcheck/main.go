// Command healthcheck probes an HTTP endpoint or the standard gRPC health API.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var err error
	if url := os.Getenv("HEALTHCHECK_HTTP_URL"); url != "" {
		err = checkHTTP(ctx, url)
	} else {
		err = checkGRPC(ctx)
	}
	if err != nil {
		os.Exit(1)
	}
}

var httpClient = http.DefaultClient

func checkHTTP(ctx context.Context, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G704 -- local operator-controlled probe
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request) // #nosec G704 -- local operator-controlled probe
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("health endpoint is not ready")
	}
	return nil
}

func checkGRPC(ctx context.Context) error {
	credentials, err := internaltls.ClientCredentials(internaltls.Files{
		CA:          os.Getenv("INTERNAL_TLS_CA_FILE"),
		Certificate: os.Getenv("HEALTHCHECK_TLS_CERT_FILE"),
		Key:         os.Getenv("HEALTHCHECK_TLS_KEY_FILE"),
	}, os.Getenv("HEALTHCHECK_TLS_SERVER_NAME"))
	if err != nil {
		return err
	}
	connection, err := grpc.NewClient(os.Getenv("HEALTHCHECK_GRPC_TARGET"), grpc.WithTransportCredentials(credentials))
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	response, err := grpc_health_v1.NewHealthClient(connection).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return errors.New("gRPC service is not serving")
	}
	return nil
}
