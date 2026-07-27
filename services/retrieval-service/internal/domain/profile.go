package domain

import (
	"crypto/sha256"
	"strconv"

	"github.com/belLena81/raglibrarian/pkg/indexprofile"
)

const (
	IndexProfileName     = indexprofile.PDFProfileName
	EPUBIndexProfileName = indexprofile.EPUBProfileName
	MediaTypePDF         = indexprofile.MediaTypePDF
	MediaTypeEPUB        = indexprofile.MediaTypeEPUB
	EmbeddingModel       = indexprofile.EmbeddingModel
	EmbeddingRevision    = indexprofile.EmbeddingRevision
	EmbeddingDimensions  = indexprofile.EmbeddingDimensions
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
	extractionVersion := indexprofile.ExtractionPDF
	switch mediaType {
	case MediaTypePDF:
	case MediaTypeEPUB:
		name = EPUBIndexProfileName
		extractionVersion = indexprofile.ExtractionEPUB
	default:
		return IndexProfile{}, false
	}
	digest := indexprofile.Digest(
		EmbeddingModel,
		EmbeddingRevision,
		strconv.Itoa(EmbeddingDimensions),
		indexprofile.DistanceCosine,
		indexprofile.PoolingCLS,
		indexprofile.NormalizationNormalized,
		indexprofile.IndexSchema,
		extractionVersion,
		indexprofile.NormalizationNFC,
		indexprofile.TokenizerCL100K,
		indexprofile.ChunkingChapterPageWindow,
		indexprofile.StructureChapterBoundary,
		strconv.Itoa(indexprofile.MaximumTokens),
		strconv.Itoa(indexprofile.OverlapTokens),
		indexprofile.ManifestSchema,
	)
	return IndexProfile{ // #nosec G101 -- this is a public model compatibility profile, not a credential.
		Name: name, Model: EmbeddingModel, Revision: EmbeddingRevision,
		Dimensions: EmbeddingDimensions, Distance: indexprofile.DistanceCosine, Pooling: indexprofile.PoolingCLS, Normalized: true,
		IndexSchema: indexprofile.IndexSchema, ExtractionVersion: extractionVersion,
		NormalizationVersion: indexprofile.NormalizationNFC, TokenizerVersion: indexprofile.TokenizerCL100K,
		ChunkingVersion: indexprofile.ChunkingChapterPageWindow, StructureVersion: indexprofile.StructureChapterBoundary,
		MaximumTokens: indexprofile.MaximumTokens, OverlapTokens: indexprofile.OverlapTokens, ManifestSchema: indexprofile.ManifestSchema,
		Digest: digest,
	}, true
}

func SupportedIndexProfileForExtraction(extractionVersion string) (IndexProfile, bool) {
	switch extractionVersion {
	case indexprofile.ExtractionPDF:
		return SupportedIndexProfileForMediaType(MediaTypePDF)
	case indexprofile.ExtractionEPUB:
		return SupportedIndexProfileForMediaType(MediaTypeEPUB)
	default:
		return IndexProfile{}, false
	}
}

// CollectionSchemaDigest freezes only the properties that affect whether
// evidence profiles can safely share a Qdrant collection.
func CollectionSchemaDigest() [sha256.Size]byte {
	return indexprofile.Digest(
		EmbeddingModel,
		EmbeddingRevision,
		strconv.Itoa(EmbeddingDimensions),
		indexprofile.DistanceCosine,
		indexprofile.IndexSchema,
	)
}
