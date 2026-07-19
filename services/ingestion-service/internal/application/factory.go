package application

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

type ProcessingFactory struct {
	tokenizer chunking.Tokenizer
	store     artifact.Store
	policy    chunking.Policy
	limits    artifact.Limits
	digest    [32]byte
}

func NewProcessingFactory(tokenizer chunking.Tokenizer, store artifact.Store, policy chunking.Policy, limits artifact.Limits) (*ProcessingFactory, error) {
	if tokenizer == nil || store == nil {
		return nil, fmt.Errorf("processing factory dependencies are required")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d", extractor.ExtractionVersion, chunking.NormalizationVersion, chunking.TokenizerVersion, chunking.ChunkingVersion, policy.MaximumTokens, policy.OverlapTokens, policy.MaximumChunks, limits.ChunksPerShard, limits.MaximumShardBytes)))
	return &ProcessingFactory{tokenizer: tokenizer, store: store, policy: policy, limits: limits, digest: digest}, nil
}

func (f *ProcessingFactory) NewChunker() (Chunker, error) {
	return chunking.New(f.tokenizer, f.policy)
}

func (f *ProcessingFactory) NewArtifactWriter(event UploadedEvent, generatedAt time.Time) (ArtifactWriter, error) {
	return artifact.NewWriter(f.store, artifact.Metadata{BookID: event.BookID, SourceSHA256: event.SourceSHA256, ConfigDigest: f.digest, GeneratedAt: generatedAt}, artifact.Versions{Extraction: extractor.ExtractionVersion, Normalization: chunking.NormalizationVersion, Tokenizer: chunking.TokenizerVersion, Chunking: chunking.ChunkingVersion}, f.limits)
}

func (f *ProcessingFactory) ConfigDigest() [32]byte { return f.digest }
