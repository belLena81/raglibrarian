package application

import (
	"fmt"
	"time"

	"github.com/belLena81/raglibrarian/pkg/indexprofile"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

type ProcessingFactory struct {
	tokenizer chunking.Tokenizer
	store     artifact.Store
	policy    chunking.Policy
	limits    artifact.Limits
	versions  map[string]string
	digests   map[string][32]byte
	selection ContentSelectionProfile
}

func NewProcessingFactory(tokenizer chunking.Tokenizer, store artifact.Store, policy chunking.Policy, limits artifact.Limits) (*ProcessingFactory, error) {
	return NewProcessingFactoryWithSelection(tokenizer, store, policy, limits, ContentSelectionProfile{Mode: ContentSelectionDisabled})
}

func NewProcessingFactoryWithSelection(tokenizer chunking.Tokenizer, store artifact.Store, policy chunking.Policy, limits artifact.Limits, selection ContentSelectionProfile) (*ProcessingFactory, error) {
	if tokenizer == nil || store == nil {
		return nil, fmt.Errorf("processing factory dependencies are required")
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	extractionVersions := map[string]string{
		MediaTypePDF:  extractor.ExtractionVersion,
		MediaTypeEPUB: extractor.EPUBExtractionVersion,
	}
	if selection.Mode != ContentSelectionDisabled {
		extractionVersions[MediaTypePDF] = indexprofile.ExtractionPDFFiltered
		extractionVersions[MediaTypeEPUB] = indexprofile.ExtractionEPUBFiltered
	}
	digests := make(map[string][32]byte, 2)
	for mediaType, extractionVersion := range extractionVersions {
		profile := indexprofile.ProcessingConfigProfile{
			ExtractionVersion:    extractionVersion,
			NormalizationVersion: chunking.NormalizationVersion,
			TokenizerVersion:     chunking.TokenizerVersion,
			ChunkingVersion:      chunking.ChunkingVersion,
			StructureVersion:     chunking.StructureVersion,
			MaximumTokens:        policy.MaximumTokens,
			OverlapTokens:        policy.OverlapTokens,
			TargetPages:          policy.TargetPages,
			MaximumPages:         policy.MaximumPages,
			MaximumChunks:        policy.MaximumChunks,
			ChunksPerShard:       limits.ChunksPerShard,
			MaximumShardBytes:    limits.MaximumShardBytes,
		}
		if selection.Mode != ContentSelectionDisabled {
			contentSelectionProfile := indexprofile.ContentSelectionProfile{
				Mode:                 indexprofile.ContentSelectionMode(selection.Mode),
				PolicyVersion:        selection.PolicyVersion,
				ParserVersion:        selection.ParserVersion,
				ModelSHA256:          selection.ModelSHA256,
				MinimumSignals:       selection.MinimumSignals,
				MaximumRanges:        selection.MaximumRanges,
				MaximumExcludedRatio: selection.MaximumExcludedRatio,
			}
			profile.ContentSelection = &contentSelectionProfile
		}
		digests[mediaType] = profile.Digest()
	}
	return &ProcessingFactory{tokenizer: tokenizer, store: store, policy: policy, limits: limits, versions: extractionVersions, digests: digests, selection: selection}, nil
}

func (f *ProcessingFactory) NewChunker() (Chunker, error) {
	return chunking.New(f.tokenizer, f.policy)
}

func (f *ProcessingFactory) NewArtifactWriter(event UploadedEvent, generatedAt time.Time) (ArtifactWriter, error) {
	digest, err := f.ConfigDigest(event.MediaType)
	if err != nil || event.ExtractionVersion == "" || event.LifecycleVersion < 1 {
		return nil, ErrUnsupportedProcessingProfile
	}
	metadata := artifact.Metadata{
		BookID:           event.BookID,
		SourceSHA256:     event.SourceSHA256,
		ConfigDigest:     digest,
		GeneratedAt:      generatedAt,
		LifecycleVersion: event.LifecycleVersion,
	}
	versions := artifact.Versions{
		Extraction:    event.ExtractionVersion,
		Normalization: chunking.NormalizationVersion,
		Tokenizer:     chunking.TokenizerVersion,
		Chunking:      chunking.ChunkingVersion,
		Structure:     chunking.StructureVersion,
	}
	profile := artifact.ProcessingProfile{
		MaximumTokens:           f.policy.MaximumTokens,
		OverlapTokens:           f.policy.OverlapTokens,
		RequireContentSelection: f.selection.Mode != ContentSelectionDisabled,
	}
	return artifact.NewWriter(f.store, metadata, versions, profile, f.limits)
}

func (f *ProcessingFactory) ConfigDigest(mediaType string) ([32]byte, error) {
	digest, found := f.digests[mediaType]
	if !found {
		return [32]byte{}, ErrUnsupportedProcessingProfile
	}
	return digest, nil
}

func (f *ProcessingFactory) ExtractionVersion(mediaType string) (string, error) {
	version, found := f.versions[mediaType]
	if !found {
		return "", ErrUnsupportedProcessingProfile
	}
	return version, nil
}

func (f *ProcessingFactory) ContentSelectionProfile() ContentSelectionProfile {
	return f.selection
}
