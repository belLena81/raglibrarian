package domain

import (
	"crypto/sha256"
	"strings"
)

const (
	IndexProfileName     = "m8-bge-base-pdf-v1"
	EPUBIndexProfileName = "m8-bge-base-epub-v1"
	MediaTypePDF         = "application/pdf"
	MediaTypeEPUB        = "application/epub+zip"
	EmbeddingModel       = "BAAI/bge-base-en-v1.5"
	EmbeddingRevision    = "5e233c43ad83ba072172bca158a7c7dec46302a0"
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
		"cls",
		"normalized",
		"retrieval-index-v2",
		extractionVersion,
		"nfc-v1",
		"cl100k_base-v1",
		"chapter-page-window-v1",
		"chapter-boundary-v1",
		"512",
		"120",
		"v1",
	}
	preimage := strings.Join(values, "\x00") + "\x00"
	return IndexProfile{ // #nosec G101 -- this is a public model compatibility profile, not a credential.
		Name: name, Model: EmbeddingModel, Revision: EmbeddingRevision,
		Dimensions: EmbeddingDimensions, Distance: "cosine", Pooling: "cls", Normalized: true,
		IndexSchema: "retrieval-index-v2", ExtractionVersion: extractionVersion,
		NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1",
		ChunkingVersion: "chapter-page-window-v1", StructureVersion: "chapter-boundary-v1",
		MaximumTokens: 512, OverlapTokens: 120, ManifestSchema: "v1",
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
