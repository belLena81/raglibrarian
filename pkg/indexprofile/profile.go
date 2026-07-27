package indexprofile

import (
	"crypto/sha256"
	"strings"
)

const (
	PDFProfileName            = "m8-bge-base-pdf-v1"
	EPUBProfileName           = "m8-bge-base-epub-v1"
	MediaTypePDF              = "application/pdf"
	MediaTypeEPUB             = "application/epub+zip"
	EmbeddingModel            = "BAAI/bge-base-en-v1.5"
	EmbeddingRevision         = "5e233c43ad83ba072172bca158a7c7dec46302a0" //gitleaks:allow -- public model revision, not a credential.
	EmbeddingDimensions       = 768
	DistanceCosine            = "cosine"
	PoolingCLS                = "cls"
	IndexSchema               = "retrieval-index-v2"
	ExtractionPDF             = "poppler-layout-v1"
	ExtractionEPUB            = "epub-spine-v1"
	NormalizationNFC          = "nfc-v1"
	NormalizationNormalized   = "normalized"
	TokenizerCL100K           = "cl100k_base-v1"
	ChunkingChapterPageWindow = "chapter-page-window-v1"
	StructureChapterBoundary  = "chapter-boundary-v1"
	MaximumTokens             = 512
	OverlapTokens             = 120
	ManifestSchema            = "v1"
)

func Digest(parts ...string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join(parts, "\x00") + "\x00"))
}
