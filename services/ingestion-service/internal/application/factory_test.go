package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
)

const m4ConfigDigestHex = "23a35a6f4f9485df637c85efa3e5b005858d3318d58ffab1c90a66cd4d4849e9"

func TestM4ProcessingProfileDigestIsStable(t *testing.T) {
	factory, err := NewProcessingFactory(factoryTokenizer{}, factoryStore{}, chunking.Policy{
		MaximumTokens: 512,
		OverlapTokens: 120,
		TargetPages:   2,
		MaximumPages:  3,
		MaximumChunks: 50_000,
	}, artifact.Limits{
		ChunksPerShard:       256,
		MaximumShardBytes:    4 << 20,
		MaximumManifestBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := factory.ConfigDigest(MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if digest := hex.EncodeToString(configDigest[:]); digest != m4ConfigDigestHex {
		t.Fatalf("M4 config digest = %q, want %q", digest, m4ConfigDigestHex)
	}
	epubDigest, err := factory.ConfigDigest(MediaTypeEPUB)
	if err != nil {
		t.Fatal(err)
	}
	if epubDigest == configDigest {
		t.Fatal("EPUB and PDF processing profiles share a digest")
	}
}

type factoryTokenizer struct{}

func (factoryTokenizer) Encode(string) []int { return nil }
func (factoryTokenizer) Decode([]int) string { return "" }

type factoryStore struct{}

func (factoryStore) Put(context.Context, string, []byte, [sha256.Size]byte) error { return nil }
func (factoryStore) Delete(context.Context, string) error                         { return nil }
