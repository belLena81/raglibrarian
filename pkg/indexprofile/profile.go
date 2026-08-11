package indexprofile

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

const (
	PDFProfileName                     = "m8-bge-base-pdf-v1"
	EPUBProfileName                    = "m8-bge-base-epub-v1"
	MediaTypePDF                       = "application/pdf"
	MediaTypeEPUB                      = "application/epub+zip"
	EmbeddingModel                     = "BAAI/bge-base-en-v1.5"
	EmbeddingRevision                  = "5e233c43ad83ba072172bca158a7c7dec46302a0" //gitleaks:allow -- public model revision, not a credential.
	EmbeddingDimensions                = 768
	DistanceCosine                     = "cosine"
	PoolingCLS                         = "cls"
	IndexSchema                        = "retrieval-index-v2"
	ExtractionPDF                      = "poppler-layout-v1"
	ExtractionEPUB                     = "epub-spine-v1"
	ExtractionPDFFiltered              = "poppler-layout-v1+layout-selector-v1"
	ExtractionEPUBFiltered             = "epub-spine-v1+layout-selector-v1"
	ContentSelectionDisabled           = "disabled"
	ContentSelectionObservation        = "observation"
	ContentSelectionEnforcement        = "enforcement"
	ContentSelectionV1                 = "layout-selector-v1"
	ContentSelectionParserBBoxLayoutV1 = "poppler-bbox-layout-v1"
	// V1 attests the exact deterministic parser profile and policy implementation.
	ContentSelectionModelSHA256          = "55b474c59ef72485c1f1238bf2793c563c49dfa28f7cec3dd298ebacd202d012" //gitleaks:allow -- public content-selection model digest, not a credential.
	ContentSelectionMinimumSignals       = 2
	ContentSelectionMaximumRanges        = 256
	ContentSelectionMaximumExcludedRatio = 0.25
	NormalizationNFC                     = "nfc-v1"
	NormalizationNormalized              = "normalized"
	TokenizerCL100K                      = "cl100k_base-v1" //nolint:gosec // G101: public tokenizer profile identifier, not a credential.
	ChunkingChapterPageWindow            = "chapter-page-window-v1"
	StructureChapterBoundary             = "chapter-boundary-v1"
	MaximumTokens                        = 512
	OverlapTokens                        = 120
	ManifestSchema                       = "v1"
)

type ContentSelectionMode string

const (
	ContentSelectionModeDisabled    ContentSelectionMode = ContentSelectionDisabled
	ContentSelectionModeObservation ContentSelectionMode = ContentSelectionObservation
	ContentSelectionModeEnforcement ContentSelectionMode = ContentSelectionEnforcement
)

type ContentSelectionProfile struct {
	Mode                 ContentSelectionMode
	PolicyVersion        string
	ParserVersion        string
	ModelSHA256          string
	MinimumSignals       int
	MaximumRanges        int
	MaximumExcludedRatio float64
}

func (p ContentSelectionProfile) Digest() [sha256.Size]byte {
	return Digest(
		string(p.Mode),
		p.PolicyVersion,
		p.ParserVersion,
		p.ModelSHA256,
		strconv.Itoa(p.MinimumSignals),
		strconv.Itoa(p.MaximumRanges),
		strconv.FormatFloat(p.MaximumExcludedRatio, 'g', -1, 64),
	)
}

type ProcessingConfigProfile struct {
	ExtractionVersion    string
	NormalizationVersion string
	TokenizerVersion     string
	ChunkingVersion      string
	StructureVersion     string
	MaximumTokens        int
	OverlapTokens        int
	TargetPages          int
	MaximumPages         int
	MaximumChunks        int
	ChunksPerShard       int
	MaximumShardBytes    int
	ContentSelection     *ContentSelectionProfile
}

func (p ProcessingConfigProfile) Digest() [sha256.Size]byte {
	parts := []string{
		p.ExtractionVersion,
		p.NormalizationVersion,
		p.TokenizerVersion,
		p.ChunkingVersion,
		p.StructureVersion,
		strconv.Itoa(p.MaximumTokens),
		strconv.Itoa(p.OverlapTokens),
		strconv.Itoa(p.TargetPages),
		strconv.Itoa(p.MaximumPages),
		strconv.Itoa(p.MaximumChunks),
		strconv.Itoa(p.ChunksPerShard),
		strconv.Itoa(p.MaximumShardBytes),
	}
	if p.ContentSelection != nil && p.ContentSelection.Mode != ContentSelectionModeDisabled {
		selectionDigest := p.ContentSelection.Digest()
		parts = append(parts, fmt.Sprintf("%x", selectionDigest))
	}
	return sha256.Sum256([]byte(strings.Join(parts, "\x00")))
}

func Digest(parts ...string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join(parts, "\x00") + "\x00"))
}
