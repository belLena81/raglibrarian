// Package transport maps stable protobuf contracts to the application boundary.
package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	ingestionconfig "github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	StartedRoute                   = contracts.EventIngestionBookProcessingStarted
	ReadyRoute                     = contracts.EventIngestionBookChunksReady
	FailedRoute                    = contracts.EventIngestionBookProcessingFailed
	ArtifactsDeletedRoute          = contracts.EventIngestionBookArtifactsDeleted
	ContentSelectionRequestedRoute = contracts.EventIngestionContentSelectionRequested
	ContentSelectionCompletedRoute = contracts.EventIngestionContentSelectionCompleted
)

func DecodeContentSelectionCompleted(payload []byte, maximumRanges int) (application.ContentSelectionResult, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes || maximumRanges < 1 || maximumRanges > 256 {
		return application.ContentSelectionResult{}, application.ErrInvalidEvent
	}
	var event ingestionv1.BookContentSelectionCompletedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &event); err != nil ||
		len(event.ProtoReflect().GetUnknown()) != 0 || event.OccurredAt == nil || !event.OccurredAt.IsValid() ||
		len(event.SourceSha256) != sha256.Size || len(event.ProcessingProfileDigest) != sha256.Size ||
		len(event.PolicyDigest) != sha256.Size || len(event.ModelDigest) != sha256.Size || allZero(event.PolicyDigest) || allZero(event.ModelDigest) ||
		!validContractVersion(event.SelectorVersion) || !validContractVersion(event.ParserVersion) ||
		len(event.ExcludedRanges) > maximumRanges || event.CausationId != event.RequestId {
		return application.ContentSelectionResult{}, application.ErrInvalidEvent
	}
	mode, valid := contentSelectionMode(event.Mode)
	if !valid {
		return application.ContentSelectionResult{}, application.ErrInvalidEvent
	}
	fallback, fallbackUnfiltered, valid := contentSelectionFallback(event.FallbackReason)
	if !valid || (fallbackUnfiltered && len(event.ExcludedRanges) != 0) {
		return application.ContentSelectionResult{}, application.ErrInvalidEvent
	}
	ranges := make([]application.ContentSelectionRange, len(event.ExcludedRanges))
	for index, item := range event.ExcludedRanges {
		if item == nil {
			return application.ContentSelectionResult{}, application.ErrInvalidEvent
		}
		reason, ok := contentExclusionReason(item.Reason)
		if !ok {
			return application.ContentSelectionResult{}, application.ErrInvalidEvent
		}
		ranges[index] = application.ContentSelectionRange{Start: item.StartOrdinal, End: item.EndOrdinal, Reason: reason}
		if index > 0 && ranges[index-1].Reason == reason && ranges[index-1].End < ^uint32(0) && item.StartOrdinal == ranges[index-1].End+1 {
			return application.ContentSelectionResult{}, application.ErrInvalidEvent
		}
	}
	result := application.ContentSelectionResult{
		EventID: event.EventId, RequestID: event.RequestId, JobID: event.JobId, BookID: event.BookId,
		CorrelationID: event.CorrelationId, CausationID: event.CausationId, Producer: event.Producer,
		SchemaVersion: event.SchemaVersion, IdempotencyKey: event.IdempotencyKey, MediaType: event.MediaType,
		Mode: mode, PolicyVersion: event.SelectorVersion, ParserVersion: event.ParserVersion,
		ModelSHA256: hex.EncodeToString(event.ModelDigest), FallbackReason: fallback,
		LifecycleVersion: event.LifecycleVersion, OriginalLocationCount: event.OriginalOrdinalCount,
		FallbackUnfiltered: fallbackUnfiltered, Ranges: ranges, OccurredAt: event.OccurredAt.AsTime(),
		PayloadDigest: sha256.Sum256(payload), Payload: append([]byte(nil), payload...),
	}
	copy(result.SourceSHA256[:], event.SourceSha256)
	copy(result.ProcessingProfileDigest[:], event.ProcessingProfileDigest)
	copy(result.PolicyDigest[:], event.PolicyDigest)
	if !validDecodedContentSelection(result, maximumRanges) {
		return application.ContentSelectionResult{}, application.ErrInvalidEvent
	}
	return result, nil
}

func validDecodedContentSelection(result application.ContentSelectionResult, maximumRanges int) bool {
	if !validContractID(result.EventID) || !validContractID(result.RequestID) || !validContractID(result.JobID) ||
		!validContractID(result.BookID) || !validContractID(result.CorrelationID) || !validContractID(result.CausationID) ||
		result.CausationID != result.RequestID || result.IdempotencyKey != result.RequestID ||
		result.Producer != "ingestion-layout-worker" || result.SchemaVersion != "v1" || result.LifecycleVersion < 1 ||
		result.OccurredAt.IsZero() || len(result.Ranges) > maximumRanges ||
		(result.MediaType != application.MediaTypePDF && result.MediaType != application.MediaTypeEPUB) ||
		result.SourceSHA256 == ([sha256.Size]byte{}) || result.ProcessingProfileDigest == ([sha256.Size]byte{}) ||
		result.PolicyDigest == ([sha256.Size]byte{}) || result.PayloadDigest != sha256.Sum256(result.Payload) {
		return false
	}
	if result.OriginalLocationCount == 0 && (!result.FallbackUnfiltered || len(result.Ranges) != 0) {
		return false
	}
	if result.Mode == string(application.ContentSelectionObservation) {
		return result.FallbackReason == "observation" && result.FallbackUnfiltered && len(result.Ranges) == 0
	}
	if result.Mode != string(application.ContentSelectionEnforcement) || result.FallbackReason == "observation" {
		return false
	}
	var previousEnd uint32
	for index, item := range result.Ranges {
		if item.Start == 0 || item.End < item.Start || item.End > result.OriginalLocationCount || (index > 0 && item.Start <= previousEnd) {
			return false
		}
		previousEnd = item.End
	}
	return !result.FallbackUnfiltered || len(result.Ranges) == 0
}

func validContractID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}

func validContractVersion(value string) bool {
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

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func DecodeUploaded(payload []byte) (application.UploadedEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.UploadedEvent{}, application.ErrInvalidEvent
	}
	var event catalogv1.BookUploadedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &event); err != nil || len(event.Sha256) != sha256Size || event.OccurredAt == nil || !event.OccurredAt.IsValid() {
		return application.UploadedEvent{}, application.ErrInvalidEvent
	}
	// Known v1 envelopes accept additive protobuf fields. The envelope remains
	// byte-bounded above and all authorization/security-relevant known fields are
	// still validated by UploadedEvent.Validate.
	lifecycleVersion := event.LifecycleVersion
	if lifecycleVersion == 0 {
		lifecycleVersion = 1
	}
	var sum [sha256Size]byte
	copy(sum[:], event.Sha256)
	return application.UploadedEvent{EventID: event.EventId, BookID: event.BookId, ObjectReference: event.ObjectReference, MediaType: event.MediaType, CorrelationID: event.CorrelationId, CausationID: event.CausationId, Producer: event.Producer, SchemaVersion: event.SchemaVersion, IdempotencyKey: event.IdempotencyKey, SourceSHA256: sum, ByteSize: event.ByteSize, LifecycleVersion: lifecycleVersion, OccurredAt: event.OccurredAt.AsTime(), Payload: append([]byte(nil), payload...)}, nil
}

func DecodeDeletion(payload []byte) (application.DeletionEvent, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes {
		return application.DeletionEvent{}, application.ErrInvalidEvent
	}
	var event catalogv1.BookDeletionRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &event); err != nil ||
		event.OccurredAt == nil || !event.OccurredAt.IsValid() {
		return application.DeletionEvent{}, application.ErrInvalidEvent
	}
	decoded := application.DeletionEvent{
		EventID:          event.EventId,
		BookID:           event.BookId,
		CommandID:        event.CommandId,
		LifecycleVersion: event.LifecycleVersion,
		CorrelationID:    event.CorrelationId,
		CausationID:      event.CausationId,
		Producer:         event.Producer,
		SchemaVersion:    event.SchemaVersion,
		IdempotencyKey:   event.IdempotencyKey,
		OccurredAt:       event.OccurredAt.AsTime(),
		Payload:          append([]byte(nil), payload...),
	}
	if err := decoded.Validate(); err != nil {
		return application.DeletionEvent{}, err
	}
	return decoded, nil
}

const sha256Size = 32

type ProtoEventFactory struct {
	newID     application.IDGenerator
	profile   chunking.Policy
	selection application.ContentSelectionProfile
}

func NewProtoEventFactory(newID application.IDGenerator) (*ProtoEventFactory, error) {
	return NewProtoEventFactoryWithProfile(newID, chunking.Policy{
		MaximumTokens: chunking.DefaultMaximumTokens,
		OverlapTokens: chunking.DefaultOverlapTokens,
		TargetPages:   ingestionconfig.DefaultChunkTargetPages,
		MaximumPages:  ingestionconfig.DefaultChunkMaximumPages,
		MaximumChunks: 1,
	})
}

func NewProtoEventFactoryWithProfile(newID application.IDGenerator, profile chunking.Policy) (*ProtoEventFactory, error) {
	return NewProtoEventFactoryWithSelection(newID, profile, application.ContentSelectionProfile{Mode: application.ContentSelectionDisabled})
}

func NewProtoEventFactoryWithSelection(newID application.IDGenerator, profile chunking.Policy, selection application.ContentSelectionProfile) (*ProtoEventFactory, error) {
	if newID == nil {
		return nil, errors.New("event ID generator is required")
	}
	if profile.MaximumTokens < 1 || profile.OverlapTokens < 0 || profile.OverlapTokens >= profile.MaximumTokens ||
		profile.TargetPages < 1 || profile.MaximumPages < profile.TargetPages {
		return nil, errors.New("invalid chunking profile")
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	return &ProtoEventFactory{newID: newID, profile: profile, selection: selection}, nil
}

func (f *ProtoEventFactory) ContentSelectionRequested(source application.UploadedEvent, job domain.ProcessingJob, now time.Time) (application.OutboxEvent, error) {
	if f.selection.Mode == application.ContentSelectionDisabled || now.IsZero() {
		return application.OutboxEvent{}, errors.New("content selection is disabled")
	}
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	processingDigest, err := hex.DecodeString(job.ConfigDigest())
	if err != nil || len(processingDigest) != sha256.Size {
		return application.OutboxEvent{}, errors.New("invalid processing profile digest")
	}
	mode, valid := contentSelectionModeProto(f.selection.Mode)
	if !valid {
		return application.OutboxEvent{}, errors.New("invalid content selection mode")
	}
	policyDigest := f.selection.PolicyDigest()
	message := &ingestionv1.BookContentSelectionRequestedV1{
		EventId: id, RequestId: id, BookId: source.BookID, SourceReference: source.ObjectReference,
		MediaType: source.MediaType, SourceSha256: source.SourceSHA256[:], SourceByteSize: source.ByteSize,
		LifecycleVersion: source.LifecycleVersion, ProcessingProfileDigest: processingDigest, Mode: mode,
		SelectorVersion: f.selection.PolicyVersion, ParserProfile: f.selection.ParserVersion,
		CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID,
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: id, JobId: job.ID(), PolicyDigest: policyDigest[:],
	}
	return marshalOutbox(id, ContentSelectionRequestedRoute, now, message)
}

func (f *ProtoEventFactory) Started(source application.UploadedEvent, job domain.ProcessingJob, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	message := &ingestionv1.BookProcessingStartedV1{EventId: id, BookId: source.BookID, SourceSha256: source.SourceSHA256[:], ExtractionVersion: source.ExtractionVersion, NormalizationVersion: chunking.NormalizationVersion, TokenizerVersion: chunking.TokenizerVersion, ChunkingVersion: chunking.ChunkingVersion, CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID, Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: fmt.Sprintf("%s:%s:started", source.BookID, job.ConfigDigest()), LifecycleVersion: source.LifecycleVersion}
	return marshalOutbox(id, StartedRoute, now, message)
}

func (f *ProtoEventFactory) Ready(source application.UploadedEvent, job domain.ProcessingJob, result artifact.Result, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	message := &ingestionv1.BookChunksReadyV1{EventId: id, BookId: source.BookID, SourceSha256: source.SourceSHA256[:], ManifestReference: result.ManifestReference, ManifestSha256: result.ManifestSHA256[:], ManifestByteSize: result.ManifestByteSize, PageCount: result.PageCount, ChunkCount: result.ChunkCount, ExtractionVersion: source.ExtractionVersion, NormalizationVersion: chunking.NormalizationVersion, TokenizerVersion: chunking.TokenizerVersion, ChunkingVersion: chunking.ChunkingVersion, StructureVersion: chunking.StructureVersion, MaximumTokens: uint32(f.profile.MaximumTokens), OverlapTokens: uint32(f.profile.OverlapTokens), CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID, Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: fmt.Sprintf("%s:%s:ready", source.BookID, job.ConfigDigest()), LifecycleVersion: source.LifecycleVersion} // #nosec G115 -- chunking profile is validated when the factory is constructed.
	return marshalOutbox(id, ReadyRoute, now, message)
}

func (f *ProtoEventFactory) Failed(source application.UploadedEvent, job domain.ProcessingJob, category domain.FailureCategory, detail string, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	protoCategory, ok := failureCategory(category)
	if !ok {
		return application.OutboxEvent{}, errors.New("invalid failure category")
	}
	message := &ingestionv1.BookProcessingFailedV1{EventId: id, BookId: source.BookID, SourceSha256: source.SourceSHA256[:], ExtractionVersion: source.ExtractionVersion, NormalizationVersion: chunking.NormalizationVersion, TokenizerVersion: chunking.TokenizerVersion, ChunkingVersion: chunking.ChunkingVersion, FailureCategory: protoCategory, CorrelationId: source.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: source.EventID, Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: fmt.Sprintf("%s:%s:failed", source.BookID, job.ConfigDigest()), LifecycleVersion: source.LifecycleVersion, FailureDetail: detail}
	return marshalOutbox(id, FailedRoute, now, message)
}

func (f *ProtoEventFactory) ArtifactsDeleted(source application.DeletionEvent, now time.Time) (application.OutboxEvent, error) {
	id, err := f.newID()
	if err != nil {
		return application.OutboxEvent{}, errors.New("generate event ID")
	}
	message := &ingestionv1.BookArtifactsDeletedV1{
		EventId:          id,
		BookId:           source.BookID,
		CommandId:        source.CommandID,
		LifecycleVersion: source.LifecycleVersion,
		CorrelationId:    source.CorrelationID,
		OccurredAt:       timestamppb.New(now),
		CausationId:      source.EventID,
		Producer:         "ingestion-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   source.CommandID,
	}
	return marshalOutbox(id, ArtifactsDeletedRoute, now, message)
}

func marshalOutbox(id, eventType string, now time.Time, message proto.Message) (application.OutboxEvent, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return application.OutboxEvent{}, errors.New("encode event")
	}
	return application.OutboxEvent{ID: id, Type: eventType, Payload: payload, OccurredAt: now}, nil
}

func failureCategory(value domain.FailureCategory) (ingestionv1.BookProcessingFailureCategory, bool) {
	values := map[domain.FailureCategory]ingestionv1.BookProcessingFailureCategory{
		domain.FailureEncryptedDocument:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_ENCRYPTED_DOCUMENT,
		domain.FailureExtractionNotPermitted:  ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_EXTRACTION_NOT_PERMITTED,
		domain.FailureMalformedDocument:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_MALFORMED_DOCUMENT,
		domain.FailureUnsupportedDocument:     ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_UNSUPPORTED_DOCUMENT,
		domain.FailureNoExtractableText:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_NO_EXTRACTABLE_TEXT,
		domain.FailureResourceLimitExceeded:   ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_RESOURCE_LIMIT_EXCEEDED,
		domain.FailureSourceIntegrityMismatch: ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_SOURCE_INTEGRITY_MISMATCH,
		domain.FailureProcessingTimeout:       ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_PROCESSING_TIMEOUT,
		domain.FailureDependencyUnavailable:   ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_DEPENDENCY_UNAVAILABLE,
		domain.FailureInternalProcessing:      ingestionv1.BookProcessingFailureCategory_BOOK_PROCESSING_FAILURE_CATEGORY_INTERNAL_PROCESSING_ERROR,
	}
	result, ok := values[value]
	return result, ok
}

func contentSelectionModeProto(value application.ContentSelectionMode) (ingestionv1.ContentSelectionMode, bool) {
	switch value {
	case application.ContentSelectionObservation:
		return ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION, true
	case application.ContentSelectionEnforcement:
		return ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT, true
	default:
		return ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_UNSPECIFIED, false
	}
}

func contentSelectionMode(value ingestionv1.ContentSelectionMode) (string, bool) {
	switch value {
	case ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION:
		return string(application.ContentSelectionObservation), true
	case ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT:
		return string(application.ContentSelectionEnforcement), true
	default:
		return "", false
	}
}

func contentExclusionReason(value ingestionv1.ContentExclusionReason) (string, bool) {
	values := map[ingestionv1.ContentExclusionReason]string{
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE:                       "title",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COPYRIGHT_OR_IMPRINT:             "copyright_imprint",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_DEDICATION_OR_ORNAMENTAL_BLANK:   "dedication_ornamental",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TABLE_OR_LIST:                    "table_of_contents",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_INDEX:                            "index",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_PUBLISHER_CATALOG_OR_ADVERTISING: "publisher_catalog_advertising",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_ALSO_BY:                          "also_by",
		ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COLOPHON:                         "colophon",
	}
	result, ok := values[value]
	return result, ok
}

func contentSelectionFallback(value ingestionv1.ContentSelectionFallbackReason) (string, bool, bool) {
	values := map[ingestionv1.ContentSelectionFallbackReason]string{
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE:                "none",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION:         "observation",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_UNSUPPORTED_LAYOUT:  "unsupported_layout",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_PROCESSING_TIMEOUT:  "processing_timeout",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT:      "invalid_output",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_AMBIGUOUS_MAPPING:   "ambiguous_mapping",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_RESOURCE_LIMIT:      "resource_limit",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_EXCESSIVE_EXCLUSION: "excessive_exclusion",
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INTERNAL_ERROR:      "internal_error",
	}
	result, ok := values[value]
	return result, value != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE, ok
}
