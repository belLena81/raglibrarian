package cataloggrpc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

func TestStorageFailuresUseAllowlistedDiagnosticsAndSanitizedUnavailableStatus(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		err        error
		wantReason string
	}{
		{name: "storage unavailable", err: fmt.Errorf("put original: %w", catalog.ErrObjectStorageUnavailable), wantReason: "object_storage_unavailable"},
		{name: "receipt mismatch", err: fmt.Errorf("verify original: %w", catalog.ErrObjectReceiptMismatch), wantReason: "object_receipt_mismatch"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := uploadFailureReason(testCase.err); got != testCase.wantReason {
				t.Fatalf("uploadFailureReason() = %q, want %q", got, testCase.wantReason)
			}
			mapped := mapError(testCase.err)
			if status.Code(mapped) != codes.Unavailable || status.Convert(mapped).Message() != "catalog unavailable" {
				t.Fatalf("mapError() = %v", mapped)
			}
			if strings.Contains(status.Convert(mapped).Message(), "originals/") {
				t.Fatal("mapped error exposed object reference")
			}
		})
	}
}

func TestUnknownFailureRemainsPersistenceUnavailable(t *testing.T) {
	if got := uploadFailureReason(errors.New("database connection lost")); got != "persistence_unavailable" {
		t.Fatalf("uploadFailureReason() = %q", got)
	}
}
