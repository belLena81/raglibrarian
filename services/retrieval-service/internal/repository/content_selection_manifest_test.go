package repository

import (
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
)

func TestFilteredManifestContentSelectionRoundTrip(t *testing.T) {
	manifest := application.Manifest{
		SchemaVersion:          "v1",
		BookID:                 "book-1",
		SourceSHA256:           selectionTestDigest(1),
		ProcessingConfigDigest: selectionTestDigest(2),
		ExtractionVersion:      "poppler-layout-v1+layout-selector-v1",
		NormalizationVersion:   "nfc-v1",
		TokenizerVersion:       "cl100k_base-v1",
		ChunkingVersion:        "chapter-page-window-v1",
		StructureVersion:       "chapter-boundary-v1",
		MaximumTokens:          512,
		OverlapTokens:          120,
		PageCount:              2,
		ChunkCount:             1,
		GeneratedAt:            time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC),
		LifecycleVersion:       1,
		Shards: []application.Shard{{
			Reference:         "books/book-1/source/profile/shards/000000.pb.zst",
			SHA256:            selectionTestDigest(3),
			CompressedBytes:   10,
			UncompressedBytes: 20,
			ChunkCount:        1,
		}},
		ContentSelection: &application.ContentSelection{
			Mode:                    "enforcement",
			SelectorVersion:         "layout-selector-v1",
			ParserVersion:           "docling-serve-v1.21.0",
			FallbackReason:          "none",
			ModelDigest:             selectionTestDigest(4),
			PolicyDigest:            selectionTestDigest(5),
			ProcessingProfileDigest: selectionTestDigest(6),
			OriginalCount:           2,
			RetainedCount:           1,
			ExcludedCount:           1,
			ExcludedRatio:           0.5,
			Ranges: []application.ContentSelectionRange{{
				Start:  1,
				End:    1,
				Reason: "title_page",
			}},
		},
	}
	payload, err := encodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeManifest(payload, selectionTestDigest(7))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ContentSelection == nil || decoded.ContentSelection.Mode != "enforcement" ||
		decoded.ContentSelection.FallbackReason != "none" || len(decoded.ContentSelection.Ranges) != 1 ||
		decoded.ContentSelection.Ranges[0].Reason != "title_page" {
		t.Fatalf("decoded content selection = %+v", decoded.ContentSelection)
	}
}

func selectionTestDigest(value byte) [32]byte {
	var digest [32]byte
	digest[0] = value
	return digest
}
