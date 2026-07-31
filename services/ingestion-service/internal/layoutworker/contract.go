package layoutworker

import (
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const Producer = "ingestion-layout-worker"

var sourceReferencePattern = regexp.MustCompile(`^originals/[A-Za-z0-9_-]{1,256}\.(pdf|epub)$`)

type Request struct {
	EventID, RequestID, JobID, BookID, SourceReference, MediaType string
	CorrelationID, SelectorVersion, ParserProfile                 string
	SourceSHA256, ProcessingProfileDigest, PolicyDigest           [sha256.Size]byte
	SourceByteSize, LifecycleVersion                              int64
	Mode                                                          ingestionv1.ContentSelectionMode
	OccurredAt                                                    time.Time
}

func DecodeRequest(payload []byte, maximumSourceBytes int64) (Request, error) {
	if len(payload) == 0 || len(payload) > contracts.MaximumBrokerMessageBytes || maximumSourceBytes < 1 {
		return Request{}, ErrInvalidRequest
	}
	var message ingestionv1.BookContentSelectionRequestedV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil ||
		len(message.ProtoReflect().GetUnknown()) != 0 || message.OccurredAt == nil || !message.OccurredAt.IsValid() {
		return Request{}, ErrInvalidRequest
	}
	request := Request{
		EventID: message.EventId, RequestID: message.RequestId, JobID: message.JobId, BookID: message.BookId,
		SourceReference: message.SourceReference, MediaType: message.MediaType, CorrelationID: message.CorrelationId,
		SelectorVersion: message.SelectorVersion, ParserProfile: message.ParserProfile,
		SourceByteSize: message.SourceByteSize, LifecycleVersion: message.LifecycleVersion, Mode: message.Mode,
		OccurredAt: message.OccurredAt.AsTime(),
	}
	copy(request.SourceSHA256[:], message.SourceSha256)
	copy(request.ProcessingProfileDigest[:], message.ProcessingProfileDigest)
	copy(request.PolicyDigest[:], message.PolicyDigest)
	if len(message.SourceSha256) != sha256.Size ||
		len(message.ProcessingProfileDigest) != sha256.Size || len(message.PolicyDigest) != sha256.Size ||
		request.SourceSHA256 == ([sha256.Size]byte{}) || request.PolicyDigest == ([sha256.Size]byte{}) ||
		request.ProcessingProfileDigest == ([sha256.Size]byte{}) || request.EventID != request.RequestID ||
		message.IdempotencyKey != request.RequestID || message.Producer != "ingestion-service" || message.SchemaVersion != "v1" ||
		!safeID(request.EventID) || !safeID(request.JobID) || !safeID(request.BookID) || !safeID(request.CorrelationID) ||
		!safeID(message.CausationId) || request.SourceByteSize < 1 || request.SourceByteSize > maximumSourceBytes ||
		request.LifecycleVersion < 1 || !safeVersion(request.SelectorVersion) || !safeVersion(request.ParserProfile) ||
		(request.Mode != ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION &&
			request.Mode != ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT) ||
		!validSourceReference(request.SourceReference, request.MediaType) {
		return Request{}, ErrInvalidRequest
	}
	return request, nil
}

type Completion struct {
	Request              Request
	EventID              string
	OriginalOrdinalCount uint32
	ParserVersion        string
	ModelDigest          [sha256.Size]byte
	PolicyDigest         [sha256.Size]byte
	FallbackReason       ingestionv1.ContentSelectionFallbackReason
	Ranges               []*ingestionv1.ExcludedRangeV1
	OccurredAt           time.Time
}

func EncodeCompletion(value Completion, maximumRanges int) ([]byte, error) {
	if !safeID(value.EventID) || value.OccurredAt.IsZero() || value.ModelDigest == ([sha256.Size]byte{}) ||
		value.PolicyDigest == ([sha256.Size]byte{}) || !safeVersion(value.ParserVersion) || maximumRanges < 1 ||
		len(value.Ranges) > maximumRanges || !validCompletion(value) {
		return nil, errors.New("invalid content selection completion")
	}
	message := &ingestionv1.BookContentSelectionCompletedV1{
		EventId: value.EventID, RequestId: value.Request.RequestID, JobId: value.Request.JobID,
		BookId: value.Request.BookID, SourceSha256: value.Request.SourceSHA256[:],
		LifecycleVersion: value.Request.LifecycleVersion, ProcessingProfileDigest: value.Request.ProcessingProfileDigest[:],
		OriginalOrdinalCount: value.OriginalOrdinalCount, Mode: value.Request.Mode,
		SelectorVersion: value.Request.SelectorVersion, ParserVersion: value.ParserVersion,
		ModelDigest: value.ModelDigest[:], PolicyDigest: value.PolicyDigest[:], FallbackReason: value.FallbackReason,
		ExcludedRanges: value.Ranges, CorrelationId: value.Request.CorrelationID, OccurredAt: timestamppb.New(value.OccurredAt),
		CausationId: value.Request.RequestID, Producer: Producer, SchemaVersion: "v1",
		IdempotencyKey: value.Request.RequestID, MediaType: value.Request.MediaType,
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

func validCompletion(value Completion) bool {
	fallback := value.FallbackReason
	if !validFallbackReason(fallback) ||
		(fallback != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE && len(value.Ranges) != 0) ||
		(fallback == ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE && value.OriginalOrdinalCount == 0) ||
		(value.Request.Mode == ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION &&
			fallback != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION) ||
		(value.Request.Mode == ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT &&
			fallback == ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION) {
		return false
	}
	var previous uint32
	for index, item := range value.Ranges {
		if item == nil || item.StartOrdinal == 0 || item.EndOrdinal < item.StartOrdinal ||
			item.EndOrdinal > value.OriginalOrdinalCount || !validExclusionReason(item.Reason) ||
			(index > 0 && item.StartOrdinal <= previous) {
			return false
		}
		previous = item.EndOrdinal
	}
	return true
}

func validFallbackReason(value ingestionv1.ContentSelectionFallbackReason) bool {
	switch value {
	case ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_UNSUPPORTED_LAYOUT,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_PROCESSING_TIMEOUT,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_AMBIGUOUS_MAPPING,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_RESOURCE_LIMIT,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_EXCESSIVE_EXCLUSION,
		ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INTERNAL_ERROR:
		return true
	default:
		return false
	}
}

func validExclusionReason(value ingestionv1.ContentExclusionReason) bool {
	return value >= ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE &&
		value <= ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COLOPHON
}

func safeID(value string) bool {
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

func safeVersion(value string) bool {
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

func validSourceReference(reference, mediaType string) bool {
	if !sourceReferencePattern.MatchString(reference) {
		return false
	}
	return (mediaType == "application/pdf" && strings.HasSuffix(reference, ".pdf")) ||
		(mediaType == "application/epub+zip" && strings.HasSuffix(reference, ".epub"))
}

var ErrInvalidRequest = errors.New("invalid content selection request")
