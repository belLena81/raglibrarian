//go:build e2e && m6

package e2e_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
)

func TestGRPCM6AnswerAcceptsOnlyEdgeCertificate(t *testing.T) {
	requireM6ContractTests(t)
	target := envOr("ANSWER_GRPC_ADDR", "answer-service:50055")

	edgeConnection := dialMTLS(t, target, "answer-service", "EDGE_TLS_CERT_FILE", "EDGE_TLS_KEY_FILE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	checked, err := answerv1.NewAnswerServiceClient(edgeConnection).Check(ctx, &answerv1.CheckRequest{})
	cancel()
	require.NoError(t, err)
	require.Equal(t, "SERVING", checked.GetStatus())

	for _, certificate := range []struct {
		name    string
		certEnv string
		keyEnv  string
	}{
		{name: "retrieval service", certEnv: "RETRIEVAL_TLS_CERT_FILE", keyEnv: "RETRIEVAL_TLS_KEY_FILE"},
		{name: "unknown client", certEnv: "UNKNOWN_TLS_CERT_FILE", keyEnv: "UNKNOWN_TLS_KEY_FILE"},
	} {
		t.Run(certificate.name, func(t *testing.T) {
			connection := dialMTLS(t, target, "answer-service", certificate.certEnv, certificate.keyEnv)
			requestContext, requestCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer requestCancel()
			_, requestErr := answerv1.NewAnswerServiceClient(connection).Check(requestContext, &answerv1.CheckRequest{})
			require.Equal(t, codes.PermissionDenied, status.Code(requestErr))
		})
	}
}

func TestGRPCM6RetrievalAcceptsAnswerCertificate(t *testing.T) {
	requireM6ContractTests(t)
	connection := dialMTLS(t, envOr("RETRIEVAL_GRPC_ADDR", "retrieval-service:50054"), "retrieval-service", "ANSWER_TLS_CERT_FILE", "ANSWER_TLS_KEY_FILE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	checked, err := retrievalv1.NewRetrievalServiceClient(connection).Check(ctx, &retrievalv1.CheckRequest{})
	require.NoError(t, err)
	require.Equal(t, "SERVING", checked.GetStatus())
}

func requireM6ContractTests(t *testing.T) {
	t.Helper()
	if os.Getenv("M6_GRPC_CONTRACT_TESTS") != "true" {
		t.Fatal("M6_GRPC_CONTRACT_TESTS=true is required")
	}
}
