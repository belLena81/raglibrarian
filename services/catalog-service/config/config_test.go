package config

import (
	"testing"
	"time"
)

func TestCatalogBounds(t *testing.T) {
	t.Setenv("CATALOG_MAX_UPLOAD_BYTES", "52428800")
	bytes, err := boundedInt64("CATALOG_MAX_UPLOAD_BYTES", 50<<20, 512<<20)
	if err != nil || bytes != 50<<20 {
		t.Fatalf("bytes = %d, err = %v", bytes, err)
	}
	t.Setenv("CATALOG_MAX_UPLOAD_BYTES", "536870913")
	if _, err := boundedInt64("CATALOG_MAX_UPLOAD_BYTES", 50<<20, 512<<20); err == nil {
		t.Fatal("expected max upload error")
	}
	t.Setenv("CATALOG_UPLOAD_CONCURRENCY", "17")
	if _, err := boundedInt("CATALOG_UPLOAD_CONCURRENCY", 2, 16); err == nil {
		t.Fatal("expected concurrency error")
	}
}

func TestPrivateMetricsAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9092", "10.0.0.10:9092", "[::1]:9092"} {
		if _, err := privateMetricsAddress(address); err != nil {
			t.Errorf("privateMetricsAddress(%q): %v", address, err)
		}
	}
	if _, err := privateMetricsAddress("8.8.8.8:9092"); err == nil {
		t.Fatal("expected public address rejection")
	}
	if _, err := privateMetricsAddress(":9092"); err == nil {
		t.Fatal("expected wildcard address rejection")
	}
}

func TestStrictBool(t *testing.T) {
	t.Setenv("CATALOG_MINIO_INSECURE", "false")
	if value, err := strictBool("CATALOG_MINIO_INSECURE", false); err != nil || value {
		t.Fatalf("strictBool() = %v, %v", value, err)
	}
	t.Setenv("CATALOG_MINIO_INSECURE", "1")
	if _, err := strictBool("CATALOG_MINIO_INSECURE", false); err == nil {
		t.Fatal("expected non-boolean value rejection")
	}
}

func TestMinIOEndpoint(t *testing.T) {
	for _, endpoint := range []string{"minio:9000", "storage.internal", "[::1]:9000"} {
		if err := validateMinIOEndpoint(endpoint); err != nil {
			t.Errorf("validateMinIOEndpoint(%q): %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"https://minio:9000", "minio:9000/path", "user@minio:9000", ""} {
		if err := validateMinIOEndpoint(endpoint); err == nil {
			t.Errorf("expected endpoint %q rejection", endpoint)
		}
	}
}

func TestBoundedDuration(t *testing.T) {
	t.Setenv("CATALOG_RECONCILE_INTERVAL", "15m")
	if value, err := boundedDuration("CATALOG_RECONCILE_INTERVAL", time.Minute, 24*time.Hour, time.Hour); err != nil || value != 15*time.Minute {
		t.Fatalf("boundedDuration() = %v, %v", value, err)
	}
	t.Setenv("CATALOG_RECONCILE_INTERVAL", "30s")
	if _, err := boundedDuration("CATALOG_RECONCILE_INTERVAL", time.Minute, 24*time.Hour, time.Hour); err == nil {
		t.Fatal("expected short duration rejection")
	}
}
