package domain

import (
	"crypto/sha256"
	"strings"
)

const (
	IndexProfileName    = "m5-jina-code-v1"
	EmbeddingModel      = "jinaai/jina-embeddings-v2-base-code"
	EmbeddingRevision   = "516f4baf13dec4ddddda8631e019b5737c8bc250"
	EmbeddingDimensions = 768
)

// IndexProfile freezes every compatibility input that affects stored evidence.
type IndexProfile struct {
	Name                 string
	Model                string
	Revision             string
	Dimensions           int
	Distance             string
	Pooling              string
	Normalized           bool
	IndexSchema          string
	ExtractionVersion    string
	NormalizationVersion string
	TokenizerVersion     string
	ChunkingVersion      string
	StructureVersion     string
	MaximumTokens        int
	OverlapTokens        int
	ManifestSchema       string
	Digest               [sha256.Size]byte
}

// SupportedIndexProfile returns M5's immutable indexing compatibility profile.
func SupportedIndexProfile() IndexProfile {
	values := []string{
		EmbeddingModel,
		EmbeddingRevision,
		"768",
		"cosine",
		"mean",
		"normalized",
		"retrieval-index-v1",
		"poppler-layout-v1",
		"nfc-v1",
		"cl100k_base-v1",
		"token-window-v2",
		"heading-carry-v1",
		"800",
		"120",
		"v1",
	}
	preimage := strings.Join(values, "\x00") + "\x00"
	return IndexProfile{ // #nosec G101 -- this is a public model compatibility profile, not a credential.
		Name: IndexProfileName, Model: EmbeddingModel, Revision: EmbeddingRevision,
		Dimensions: EmbeddingDimensions, Distance: "cosine", Pooling: "mean", Normalized: true,
		IndexSchema: "retrieval-index-v1", ExtractionVersion: "poppler-layout-v1",
		NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1",
		ChunkingVersion: "token-window-v2", StructureVersion: "heading-carry-v1",
		MaximumTokens: 800, OverlapTokens: 120, ManifestSchema: "v1",
		Digest: sha256.Sum256([]byte(preimage)),
	}
}
