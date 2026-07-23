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
	digests   map[string][32]byte
}

func NewProcessingFactory(tokenizer chunking.Tokenizer, store artifact.Store, policy chunking.Policy, limits artifact.Limits) (*ProcessingFactory, error) {
	if tokenizer == nil || store == nil {
		return nil, fmt.Errorf("processing factory dependencies are required")
	}
	digests := make(map[string][32]byte, 2)
	for mediaType, extractionVersion := range map[string]string{
		MediaTypePDF:  extractor.ExtractionVersion,
		MediaTypeEPUB: extractor.EPUBExtractionVersion,
	} {
		digests[mediaType] = sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d", extractionVersion, chunking.NormalizationVersion, chunking.TokenizerVersion, chunking.ChunkingVersion, chunking.StructureVersion, policy.MaximumTokens, policy.OverlapTokens, policy.MaximumChunks, limits.ChunksPerShard, limits.MaximumShardBytes)))
	}
	return &ProcessingFactory{tokenizer: tokenizer, store: store, policy: policy, limits: limits, digests: digests}, nil
}

func (f *ProcessingFactory) NewChunker() (Chunker, error) {
	return chunking.New(f.tokenizer, f.policy)
}

func (f *ProcessingFactory) NewArtifactWriter(event UploadedEvent, generatedAt time.Time) (ArtifactWriter, error) {
	digest, err := f.ConfigDigest(event.MediaType)
	if err != nil || event.ExtractionVersion == "" || event.LifecycleVersion < 1 {
		return nil, ErrUnsupportedProcessingProfile
	}
	return artifact.NewWriter(f.store, artifact.Metadata{BookID: event.BookID, SourceSHA256: event.SourceSHA256, ConfigDigest: digest, GeneratedAt: generatedAt, LifecycleVersion: event.LifecycleVersion}, artifact.Versions{Extraction: event.ExtractionVersion, Normalization: chunking.NormalizationVersion, Tokenizer: chunking.TokenizerVersion, Chunking: chunking.ChunkingVersion, Structure: chunking.StructureVersion}, artifact.ProcessingProfile{MaximumTokens: f.policy.MaximumTokens, OverlapTokens: f.policy.OverlapTokens}, f.limits)
}

func (f *ProcessingFactory) ConfigDigest(mediaType string) ([32]byte, error) {
	digest, found := f.digests[mediaType]
	if !found {
		return [32]byte{}, ErrUnsupportedProcessingProfile
	}
	return digest, nil
}
