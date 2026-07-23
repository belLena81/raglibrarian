package main

import (
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/serverless"
)

func TestValidQueue(t *testing.T) {
	for _, queue := range []string{serverless.MetadataQueue, serverless.ManifestQueue, serverless.IndexQueue, serverless.LifecycleQueue} {
		if !validQueue(queue) {
			t.Fatalf("validQueue(%q) = false", queue)
		}
	}
	if validQueue("other") {
		t.Fatal("validQueue(other) = true")
	}
}
