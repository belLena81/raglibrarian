package domain

import (
	"crypto/sha256"
	"strings"
)

const (
	IndexProfileName     = "m5-jina-code-v1"
	EPUBIndexProfileName = "m7-jina-code-epub-v1"
	MediaTypePDF         = "application/pdf"
	MediaTypeEPUB        = "application/epub+zip"
	EmbeddingModel       = "jinaai/jina-embeddings-v2-base-code"
	EmbeddingRevision    = "516f4baf13dec4ddddda8631e019b5737c8bc250"
	EmbeddingDimensions  = 768
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
	profile, _ := SupportedIndexProfileForMediaType(MediaTypePDF)
	return profile
}

// SupportedIndexProfileForMediaType returns the exact evidence profile for a
// trusted Catalog media type. Physical vector collection compatibility is
// intentionally represented separately by CollectionSchemaDigest.
func SupportedIndexProfileForMediaType(mediaType string) (IndexProfile, bool) {
	name := IndexProfileName
	extractionVersion := "poppler-layout-v1"
	switch mediaType {
	case MediaTypePDF:
	case MediaTypeEPUB:
		name = EPUBIndexProfileName
		extractionVersion = "epub-spine-v1"
	default:
		return IndexProfile{}, false
	}
	values := []string{
		EmbeddingModel,
		EmbeddingRevision,
		"768",
		"cosine",
		"mean",
		"normalized",
		"retrieval-index-v2",
		extractionVersion,
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
		Name: name, Model: EmbeddingModel, Revision: EmbeddingRevision,
		Dimensions: EmbeddingDimensions, Distance: "cosine", Pooling: "mean", Normalized: true,
		IndexSchema: "retrieval-index-v2", ExtractionVersion: extractionVersion,
		NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1",
		ChunkingVersion: "token-window-v2", StructureVersion: "heading-carry-v1",
		MaximumTokens: 800, OverlapTokens: 120, ManifestSchema: "v1",
		Digest: sha256.Sum256([]byte(preimage)),
	}, true
}

func SupportedIndexProfileForExtraction(extractionVersion string) (IndexProfile, bool) {
	switch extractionVersion {
	case "poppler-layout-v1":
		return SupportedIndexProfileForMediaType(MediaTypePDF)
	case "epub-spine-v1":
		return SupportedIndexProfileForMediaType(MediaTypeEPUB)
	default:
		return IndexProfile{}, false
	}
}

// CollectionSchemaDigest freezes only the properties that affect whether
// evidence profiles can safely share a Qdrant collection.
func CollectionSchemaDigest() [sha256.Size]byte {
	values := []string{
		EmbeddingModel,
		EmbeddingRevision,
		"768",
		"cosine",
		"retrieval-index-v2",
	}
	return sha256.Sum256([]byte(strings.Join(values, "\x00") + "\x00"))
}
