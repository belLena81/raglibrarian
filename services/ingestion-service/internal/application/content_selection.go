package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/selection"
)

type ContentSelectionMode string

const (
	ContentSelectionDisabled    ContentSelectionMode = "disabled"
	ContentSelectionObservation ContentSelectionMode = "observation"
	ContentSelectionEnforcement ContentSelectionMode = "enforcement"
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

func (p ContentSelectionProfile) Validate() error {
	if p.Mode != ContentSelectionDisabled && p.Mode != ContentSelectionObservation && p.Mode != ContentSelectionEnforcement {
		return errors.New("invalid content selection mode")
	}
	if p.Mode == ContentSelectionDisabled {
		return nil
	}
	if !safeProfileValue(p.PolicyVersion) || !safeProfileValue(p.ParserVersion) || len(p.ModelSHA256) != sha256.Size*2 || p.MinimumSignals < 2 || p.MinimumSignals > 4 || p.MaximumRanges < 1 || p.MaximumRanges > 1000 || math.IsNaN(p.MaximumExcludedRatio) || math.IsInf(p.MaximumExcludedRatio, 0) || p.MaximumExcludedRatio <= 0 || p.MaximumExcludedRatio > 0.5 {
		return errors.New("invalid content selection profile")
	}
	if _, err := hex.DecodeString(p.ModelSHA256); err != nil {
		return errors.New("invalid content selection model digest")
	}
	return nil
}

func (p ContentSelectionProfile) Digest() [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		string(p.Mode),
		p.PolicyVersion,
		p.ParserVersion,
		p.ModelSHA256,
		strconv.Itoa(p.MinimumSignals),
		strconv.Itoa(p.MaximumRanges),
		strconv.FormatFloat(p.MaximumExcludedRatio, 'g', -1, 64),
	}, "\x00") + "\x00"))
}

func (p ContentSelectionProfile) PolicyDigest() [sha256.Size]byte {
	return (selection.Policy{
		Version:              p.PolicyVersion,
		MinimumSignals:       p.MinimumSignals,
		MaximumExcludedRatio: p.MaximumExcludedRatio,
		MaximumRanges:        p.MaximumRanges,
	}).Digest()
}

type ContentSelectionRange struct {
	Start  uint32
	End    uint32
	Reason string
}

type ContentSelectionResult struct {
	EventID, RequestID, JobID, BookID, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	SourceSHA256, ProcessingProfileDigest, PolicyDigest, PayloadDigest                                     [sha256.Size]byte
	MediaType, Mode, PolicyVersion, ParserVersion, ModelSHA256, FallbackReason                             string
	LifecycleVersion                                                                                       int64
	OriginalLocationCount                                                                                  uint32
	FallbackUnfiltered                                                                                     bool
	Ranges                                                                                                 []ContentSelectionRange
	OccurredAt                                                                                             time.Time
	Payload                                                                                                []byte
}

func (r ContentSelectionResult) Validate(profile ContentSelectionProfile) error {
	if err := profile.Validate(); err != nil || profile.Mode == ContentSelectionDisabled {
		return ErrInvalidEvent
	}
	if !safeID(r.EventID) || !safeID(r.RequestID) || !safeID(r.JobID) || !safeID(r.BookID) || !safeID(r.CorrelationID) || !safeID(r.CausationID) || r.Producer != "ingestion-layout-worker" || r.SchemaVersion != "v1" || r.IdempotencyKey != r.RequestID || r.CausationID != r.RequestID || r.LifecycleVersion < 1 || r.OccurredAt.IsZero() || len(r.Payload) == 0 || r.SourceSHA256 == ([sha256.Size]byte{}) || r.ProcessingProfileDigest == ([sha256.Size]byte{}) || r.PolicyDigest == ([sha256.Size]byte{}) || len(r.Ranges) > profile.MaximumRanges {
		return ErrInvalidEvent
	}
	if r.MediaType != MediaTypePDF && r.MediaType != MediaTypeEPUB {
		return ErrInvalidEvent
	}
	if r.Mode != string(profile.Mode) || r.PolicyVersion != profile.PolicyVersion || r.ParserVersion != profile.ParserVersion || r.ModelSHA256 != profile.ModelSHA256 || r.PolicyDigest != profile.PolicyDigest() || sha256.Sum256(r.Payload) != r.PayloadDigest {
		return ErrInvalidEvent
	}
	if !validSelectionFallback(r.FallbackReason) || r.FallbackUnfiltered != (r.FallbackReason != "none") {
		return ErrInvalidEvent
	}
	if r.OriginalLocationCount == 0 && (!r.FallbackUnfiltered || len(r.Ranges) != 0) {
		return ErrInvalidEvent
	}
	if profile.Mode == ContentSelectionObservation && (r.FallbackReason != "observation" || len(r.Ranges) != 0) {
		return ErrInvalidEvent
	}
	if profile.Mode == ContentSelectionEnforcement && r.FallbackReason == "observation" {
		return ErrInvalidEvent
	}
	var previousEnd uint32
	var excluded uint64
	for index, value := range r.Ranges {
		if value.Start == 0 || value.End < value.Start || !validSelectionReason(value.Reason) || value.End > r.OriginalLocationCount || (index > 0 && value.Start <= previousEnd) {
			return ErrInvalidEvent
		}
		excluded += uint64(value.End) - uint64(value.Start) + 1
		previousEnd = value.End
	}
	if (r.FallbackUnfiltered && len(r.Ranges) != 0) || (r.OriginalLocationCount > 0 && float64(excluded)/float64(r.OriginalLocationCount) > profile.MaximumExcludedRatio) {
		return ErrInvalidEvent
	}
	return nil
}

type ContentSelectionRecord struct {
	EventID                 string
	RequestID               string
	JobID                   string
	BookID                  string
	PayloadDigest           [sha256.Size]byte
	Payload                 []byte
	SourceSHA256            [sha256.Size]byte
	ProcessingProfileDigest [sha256.Size]byte
	LifecycleVersion        int64
	ReceivedAt              time.Time
	AcceptedAt              time.Time
}

type UploadDecoder func([]byte) (UploadedEvent, error)
type ContentSelectionDecoder func([]byte, int) (ContentSelectionResult, error)

var ErrContentSelectionNotFound = errors.New("content selection not found")

func (r ContentSelectionResult) record(receivedAt time.Time) ContentSelectionRecord {
	return ContentSelectionRecord{
		EventID: r.EventID, RequestID: r.RequestID, JobID: r.JobID, BookID: r.BookID,
		PayloadDigest: r.PayloadDigest, Payload: append([]byte(nil), r.Payload...), SourceSHA256: r.SourceSHA256,
		ProcessingProfileDigest: r.ProcessingProfileDigest, LifecycleVersion: r.LifecycleVersion, ReceivedAt: receivedAt,
	}
}

func decodeStoredSelection(ctx context.Context, repository Repository, decoder ContentSelectionDecoder, jobID string, maximumRanges int) (ContentSelectionResult, error) {
	record, err := repository.LoadContentSelection(ctx, jobID)
	if err != nil {
		return ContentSelectionResult{}, err
	}
	result, err := decoder(record.Payload, maximumRanges)
	if err != nil || result.PayloadDigest != record.PayloadDigest || result.EventID != record.EventID || result.RequestID != record.RequestID || result.JobID != record.JobID || result.BookID != record.BookID || result.SourceSHA256 != record.SourceSHA256 || result.ProcessingProfileDigest != record.ProcessingProfileDigest || result.LifecycleVersion != record.LifecycleVersion {
		return ContentSelectionResult{}, ErrInvalidEvent
	}
	return result, nil
}

func (p *Processor) validateSelectionForJob(event UploadedEvent, job domain.ProcessingJob, result ContentSelectionResult) error {
	if err := result.Validate(p.selection); err != nil {
		return err
	}
	if err := validateSelectionForEvent(event, result); err != nil {
		return err
	}
	configDigestBytes, err := hex.DecodeString(job.ConfigDigest())
	if err != nil || len(configDigestBytes) != sha256.Size {
		return ErrUnsupportedProcessingProfile
	}
	var configDigest [sha256.Size]byte
	copy(configDigest[:], configDigestBytes)
	if result.JobID != job.ID() || result.ProcessingProfileDigest != configDigest {
		return ErrConflictingEvent
	}
	return nil
}

func validateSelectionForEvent(event UploadedEvent, result ContentSelectionResult) error {
	if result.BookID != event.BookID || result.SourceSHA256 != event.SourceSHA256 ||
		result.LifecycleVersion != event.LifecycleVersion || result.MediaType != event.MediaType ||
		result.CorrelationID != event.CorrelationID {
		return ErrConflictingEvent
	}
	return nil
}

func (p *Processor) selectionAudit(result *ContentSelectionResult, originalLocationCount uint32) *artifact.ContentSelection {
	if result == nil {
		return nil
	}
	modelBytes, err := hex.DecodeString(result.ModelSHA256)
	if err != nil || len(modelBytes) != sha256.Size {
		return nil
	}
	var modelDigest [sha256.Size]byte
	copy(modelDigest[:], modelBytes)
	ranges := make([]artifact.ContentSelectionRange, 0, len(result.Ranges))
	if p.selection.Mode == ContentSelectionEnforcement && result.FallbackReason == "none" {
		for _, item := range result.Ranges {
			ranges = append(ranges, artifact.ContentSelectionRange{Start: item.Start, End: item.End, Reason: item.Reason})
		}
	}
	fallback := result.FallbackReason
	if p.selection.Mode == ContentSelectionObservation {
		fallback = "observation"
	}
	return &artifact.ContentSelection{
		Mode: string(p.selection.Mode), SelectorVersion: result.PolicyVersion, ParserVersion: result.ParserVersion,
		ModelDigest: modelDigest, PolicyDigest: result.PolicyDigest, ProcessingProfileDigest: result.ProcessingProfileDigest,
		FallbackReason: fallback, OriginalLocationCount: originalLocationCount, Ranges: ranges,
	}
}

func validSelectionFallback(value string) bool {
	switch value {
	case "none", "observation", "unsupported_layout", "processing_timeout", "invalid_output", "ambiguous_mapping", "resource_limit", "excessive_exclusion", "internal_error":
		return true
	default:
		return false
	}
}

func validSelectionReason(value string) bool {
	switch value {
	case "title", "copyright_imprint", "dedication_ornamental", "table_or_list", "table_of_contents", "list_of_figures_tables", "index", "publisher_catalog_advertising", "also_by", "colophon":
		return true
	default:
		return false
	}
}

func safeProfileValue(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char > 0x7e {
			return false
		}
	}
	return true
}
