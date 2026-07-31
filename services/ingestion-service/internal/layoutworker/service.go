package layoutworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"time"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/layout"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/selection"
)

type SourceReader interface {
	Open(context.Context, string) (io.ReadCloser, int64, error)
}

type Analyzer interface {
	Analyze(context.Context, string, string) (layout.Document, error)
}

type Config struct {
	MaximumSourceBytes   int64
	MaximumRanges        int
	MaximumExcludedRatio float64
	MinimumSignals       int
	PolicyVersion        string
	ParserVersion        string
	ModelSHA256          string
	ParserTimeout        time.Duration
}

type Service struct {
	sources     SourceReader
	analyzer    Analyzer
	config      Config
	policy      selection.Policy
	modelDigest [sha256.Size]byte
}

func NewService(sources SourceReader, analyzer Analyzer, config Config) (*Service, error) {
	if sources == nil || analyzer == nil || config.MaximumSourceBytes < 1 || config.MaximumRanges < 1 ||
		config.MaximumRanges > 256 || config.PolicyVersion != selection.PolicyVersionV1 || !safeVersion(config.ParserVersion) ||
		config.ParserTimeout <= 0 {
		return nil, errors.New("invalid layout worker configuration")
	}
	model, err := hex.DecodeString(config.ModelSHA256)
	if err != nil || len(model) != sha256.Size {
		return nil, errors.New("invalid layout model digest")
	}
	policy := selection.Policy{Version: config.PolicyVersion, MinimumSignals: config.MinimumSignals, MaximumExcludedRatio: config.MaximumExcludedRatio, MaximumRanges: config.MaximumRanges}
	probe := selection.Decide(policy, 1, nil)
	if probe.Fallback == selection.FallbackInvalidPolicy {
		return nil, errors.New("invalid layout selection policy")
	}
	service := &Service{sources: sources, analyzer: analyzer, config: config, policy: policy}
	copy(service.modelDigest[:], model)
	return service, nil
}

// Process returns a sanitized, deterministic completion payload. Parser and
// policy, parser, and source failures fail open without disclosing object references.
func (s *Service) Process(ctx context.Context, payload []byte) (string, []byte, error) {
	request, err := DecodeRequest(payload, s.config.MaximumSourceBytes)
	if err != nil {
		return "", nil, err
	}
	policyDigest := s.policy.Digest()
	if request.SelectorVersion != s.config.PolicyVersion || request.ParserProfile != s.config.ParserVersion ||
		!equalDigest(request.PolicyDigest[:], policyDigest[:]) {
		return "", nil, ErrInvalidRequest
	}
	eventID := completionEventID(request.RequestID)
	completion := Completion{
		Request: request, EventID: eventID, ParserVersion: s.config.ParserVersion,
		ModelDigest: s.modelDigest, PolicyDigest: policyDigest, OccurredAt: request.OccurredAt.UTC(),
	}
	path, err := s.download(ctx, request)
	if err != nil {
		if request.Mode == ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION {
			completion.FallbackReason = ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION
		} else {
			completion.FallbackReason = sourceFallback(ctx, err)
		}
		encoded, encodeErr := EncodeCompletion(completion, s.config.MaximumRanges)
		return eventID, encoded, encodeErr
	}
	defer func() { _ = os.Remove(path) }()
	analyzeCtx, cancel := context.WithTimeout(ctx, s.config.ParserTimeout)
	document, analyzeErr := s.analyzer.Analyze(analyzeCtx, path, request.MediaType)
	cancel()
	if analyzeErr != nil {
		if request.Mode == ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION {
			completion.FallbackReason = ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION
		} else {
			completion.FallbackReason = parserFallback(analyzeCtx)
		}
		encoded, encodeErr := EncodeCompletion(completion, s.config.MaximumRanges)
		return eventID, encoded, encodeErr
	}
	if len(document.Locations) == 0 || uint64(len(document.Locations)) > uint64(^uint32(0)) {
		completion.FallbackReason = ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT
		encoded, encodeErr := EncodeCompletion(completion, s.config.MaximumRanges)
		return eventID, encoded, encodeErr
	}
	completion.OriginalOrdinalCount = uint32(len(document.Locations)) // #nosec G115 -- explicitly bounded above.
	if request.Mode == ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION {
		completion.FallbackReason = ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION
	} else {
		decision := selection.Decide(s.policy, completion.OriginalOrdinalCount, Classify(document))
		completion.FallbackReason = selectionFallback(decision.Fallback)
		if completion.FallbackReason == ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE {
			completion.Ranges = encodeRanges(decision.Ranges)
		}
	}
	encoded, err := EncodeCompletion(completion, s.config.MaximumRanges)
	if err != nil {
		return eventID, nil, err
	}
	return eventID, encoded, nil
}

func (s *Service) download(ctx context.Context, request Request) (string, error) {
	reader, size, err := s.sources.Open(ctx, request.SourceReference)
	if err != nil {
		return "", ErrSourceUnavailable
	}
	defer func() { _ = reader.Close() }()
	if size != request.SourceByteSize || size < 1 || size > s.config.MaximumSourceBytes {
		return "", ErrSourceIntegrity
	}
	extension := ".pdf"
	if request.MediaType == "application/epub+zip" {
		extension = ".epub"
	}
	file, err := os.CreateTemp("", "raglibrarian-layout-*"+extension)
	if err != nil {
		return "", ErrSourceUnavailable
	}
	path := file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, s.config.MaximumSourceBytes+1))
	if copyErr != nil || written != size || written != request.SourceByteSize || written > s.config.MaximumSourceBytes {
		err = ErrSourceIntegrity
		return "", err
	}
	if !equalDigest(hash.Sum(nil), request.SourceSHA256[:]) {
		err = ErrSourceIntegrity
		return "", err
	}
	if closeErr := file.Close(); closeErr != nil {
		err = ErrSourceUnavailable
		return "", err
	}
	return path, nil
}

func completionEventID(requestID string) string {
	digest := sha256.Sum256([]byte("content-selection-completed-v1\x00" + requestID))
	return "selection-" + hex.EncodeToString(digest[:16])
}

func equalDigest(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func parserFallback(ctx context.Context) ingestionv1.ContentSelectionFallbackReason {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_PROCESSING_TIMEOUT
	}
	return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT
}

func sourceFallback(ctx context.Context, err error) ingestionv1.ContentSelectionFallbackReason {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_PROCESSING_TIMEOUT
	}
	if errors.Is(err, ErrSourceIntegrity) {
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT
	}
	return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_RESOURCE_LIMIT
}

func selectionFallback(value selection.Fallback) ingestionv1.ContentSelectionFallbackReason {
	switch value {
	case selection.FallbackNone:
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE
	case selection.FallbackExcludedRatio:
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_EXCESSIVE_EXCLUSION
	case selection.FallbackRangeLimit:
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_RESOURCE_LIMIT
	case selection.FallbackInvalidInput:
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_AMBIGUOUS_MAPPING
	default:
		return ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INTERNAL_ERROR
	}
}

func encodeRanges(values []selection.Range) []*ingestionv1.ExcludedRangeV1 {
	result := make([]*ingestionv1.ExcludedRangeV1, 0, len(values))
	for _, value := range values {
		reason := exclusionReason(value.Reason)
		last := len(result) - 1
		if last >= 0 && result[last].Reason == reason && result[last].EndOrdinal < ^uint32(0) && value.Start == result[last].EndOrdinal+1 {
			result[last].EndOrdinal = value.End
			continue
		}
		result = append(result, &ingestionv1.ExcludedRangeV1{StartOrdinal: value.Start, EndOrdinal: value.End, Reason: reason})
	}
	return result
}

func exclusionReason(value selection.Reason) ingestionv1.ContentExclusionReason {
	values := map[selection.Reason]ingestionv1.ContentExclusionReason{
		selection.ReasonTitle:                ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE,
		selection.ReasonCopyright:            ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COPYRIGHT_OR_IMPRINT,
		selection.ReasonDedicationOrnamental: ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_DEDICATION_OR_ORNAMENTAL_BLANK,
		selection.ReasonTableOfContents:      ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TABLE_OR_LIST,
		selection.ReasonListOfFiguresTables:  ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TABLE_OR_LIST,
		selection.ReasonIndex:                ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_INDEX,
		selection.ReasonPublisherCatalog:     ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_PUBLISHER_CATALOG_OR_ADVERTISING,
		selection.ReasonAlsoBy:               ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_ALSO_BY,
		selection.ReasonColophon:             ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COLOPHON,
	}
	return values[value]
}

var (
	ErrSourceUnavailable = errors.New("layout source unavailable")
	ErrSourceIntegrity   = errors.New("layout source integrity mismatch")
)
