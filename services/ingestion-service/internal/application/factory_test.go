package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/indexprofile"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
)

func TestProcessingFactoryExtractionVersionFollowsSelectionMode(t *testing.T) {
	policy := chunking.Policy{
		MaximumTokens: 512,
		OverlapTokens: 120,
		TargetPages:   2,
		MaximumPages:  3,
		MaximumChunks: 50_000,
	}
	limits := artifact.Limits{
		ChunksPerShard:       256,
		MaximumShardBytes:    4 << 20,
		MaximumManifestBytes: 1 << 20,
	}
	filteredProfile := ContentSelectionProfile{
		Mode:                 ContentSelectionEnforcement,
		PolicyVersion:        "layout-selector-v1",
		ParserVersion:        "poppler-layout-v1",
		ModelSHA256:          "0123456789012345678901234567890123456789012345678901234567890123",
		MinimumSignals:       2,
		MaximumRanges:        100,
		MaximumExcludedRatio: 0.5,
	}

	tests := []struct {
		name      string
		selection ContentSelectionProfile
		mediaType string
		want      string
	}{
		{name: "disabled PDF", selection: ContentSelectionProfile{Mode: ContentSelectionDisabled}, mediaType: MediaTypePDF, want: indexprofile.ExtractionPDF},
		{name: "disabled EPUB", selection: ContentSelectionProfile{Mode: ContentSelectionDisabled}, mediaType: MediaTypeEPUB, want: indexprofile.ExtractionEPUB},
		{name: "enforcement PDF", selection: filteredProfile, mediaType: MediaTypePDF, want: indexprofile.ExtractionPDFFiltered},
		{name: "enforcement EPUB", selection: filteredProfile, mediaType: MediaTypeEPUB, want: indexprofile.ExtractionEPUBFiltered},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory, err := NewProcessingFactoryWithSelection(factoryTokenizer{}, factoryStore{}, policy, limits, test.selection)
			if err != nil {
				t.Fatal(err)
			}
			version, err := factory.ExtractionVersion(test.mediaType)
			if err != nil {
				t.Fatal(err)
			}
			if version != test.want {
				t.Fatalf("extraction version = %q, want %q", version, test.want)
			}
		})
	}
}

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

func TestFactoryRequiresContentSelectionAuditForEnabledSelection(t *testing.T) {
	factory, err := NewProcessingFactoryWithSelection(factoryTokenizer{}, factoryStore{}, chunking.Policy{
		MaximumTokens: 512,
		OverlapTokens: 120,
		TargetPages:   2,
		MaximumPages:  3,
		MaximumChunks: 50_000,
	}, artifact.Limits{
		ChunksPerShard:       256,
		MaximumShardBytes:    4 << 20,
		MaximumManifestBytes: 1 << 20,
	}, ContentSelectionProfile{
		Mode:                 ContentSelectionEnforcement,
		PolicyVersion:        "layout-selector-v1",
		ParserVersion:        "poppler-layout-v1",
		ModelSHA256:          "0123456789012345678901234567890123456789012345678901234567890123",
		MinimumSignals:       2,
		MaximumRanges:        100,
		MaximumExcludedRatio: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := UploadedEvent{
		BookID:            "book-1",
		MediaType:         MediaTypePDF,
		SourceSHA256:      sha256.Sum256([]byte("source")),
		ExtractionVersion: "poppler-layout-v1+layout-selector-v1",
		LifecycleVersion:  1,
	}
	writer, err := factory.NewArtifactWriter(event, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := domain.NewChunk(domain.ChunkInput{
		ID: "chunk-1", BookID: event.BookID, Text: "retained",
		PageStart: 1, PageEnd: 1, TokenEnd: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Add(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Finalize(context.Background(), 1, nil); err == nil {
		t.Fatal("enabled selection finalized without an audit")
	}
}

type factoryTokenizer struct{}

func (factoryTokenizer) Encode(string) []int { return nil }
func (factoryTokenizer) Decode([]int) string { return "" }

type factoryStore struct{}

func (factoryStore) Put(context.Context, string, []byte, [sha256.Size]byte) error { return nil }
func (factoryStore) Delete(context.Context, string) error                         { return nil }
