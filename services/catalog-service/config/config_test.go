package config

import "testing"

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
	for _, address := range []string{":9092", "127.0.0.1:9092", "10.0.0.10:9092", "[::1]:9092"} {
		if _, err := privateMetricsAddress(address); err != nil {
			t.Errorf("privateMetricsAddress(%q): %v", address, err)
		}
	}
	if _, err := privateMetricsAddress("8.8.8.8:9092"); err == nil {
		t.Fatal("expected public address rejection")
	}
}
