package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerExposesOnlyFixedMetrics(t *testing.T) {
	recorder := New(nil)
	recorder.OutboxPublishFailed()
	recorder.ReconciliationCompleted(12, 2)
	recorder.SetReadiness(true, false)
	recorder.SetOutboxBacklog(3, 42)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"catalog_outbox_publish_failures_total 1",
		"catalog_reconciliation_scanned_total 12",
		"catalog_reconciliation_deleted_total 2",
		"catalog_postgres_ready 1",
		"catalog_minio_ready 0",
		"catalog_outbox_pending 3",
		"catalog_outbox_oldest_age_seconds 42",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q in %q", expected, body)
		}
	}
	if strings.ContainsAny(body, "{}") {
		t.Fatal("metrics must not contain dynamic labels")
	}
}

func TestHandlerRejectsOtherPaths(t *testing.T) {
	response := httptest.NewRecorder()
	New(nil).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}
