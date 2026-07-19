package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
)

const m4ConfigDigestHex = "bf78af147282f437086fe289afc14968ef7e20db0546c63672369e6530a18add"

func TestM4ProcessingProfileDigestIsStable(t *testing.T) {
	factory, err := NewProcessingFactory(factoryTokenizer{}, factoryStore{}, chunking.Policy{
		MaximumTokens: 800,
		OverlapTokens: 120,
		MaximumChunks: 50_000,
	}, artifact.Limits{
		ChunksPerShard:       256,
		MaximumShardBytes:    4 << 20,
		MaximumManifestBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	configDigest := factory.ConfigDigest()
	if digest := hex.EncodeToString(configDigest[:]); digest != m4ConfigDigestHex {
		t.Fatalf("M4 config digest = %q, want %q", digest, m4ConfigDigestHex)
	}
}

type factoryTokenizer struct{}

func (factoryTokenizer) Encode(string) []int { return nil }
func (factoryTokenizer) Decode([]int) string { return "" }

type factoryStore struct{}

func (factoryStore) Put(context.Context, string, []byte, [sha256.Size]byte) error { return nil }
func (factoryStore) Delete(context.Context, string) error                         { return nil }
