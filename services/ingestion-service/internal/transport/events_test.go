package transport

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBuildContentSelectionRequestUsesOneDurableRequestIdentity(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	profile := application.ContentSelectionProfile{
		Mode:                 application.ContentSelectionEnforcement,
		PolicyVersion:        "layout-selector-v1",
		ParserVersion:        "docling-serve-v1.21.0",
		ModelSHA256:          bytesToHex(bytes.Repeat([]byte{7}, sha256.Size)),
		MinimumSignals:       2,
		MaximumRanges:        256,
		MaximumExcludedRatio: 0.25,
	}
	factory, err := NewProtoEventFactoryWithSelection(func() (string, error) { return "selection-request-1", nil }, chunking.Policy{
		MaximumTokens: chunking.DefaultMaximumTokens,
		OverlapTokens: chunking.DefaultOverlapTokens,
		TargetPages:   2,
		MaximumPages:  3,
		MaximumChunks: 100,
	}, profile)
	if err != nil {
		t.Fatal(err)
	}
	source := application.UploadedEvent{
		EventID: "upload-1", BookID: "book-1", ObjectReference: "originals/book-1.pdf", MediaType: application.MediaTypePDF,
		SourceSHA256: [sha256.Size]byte{1}, ByteSize: 1234, LifecycleVersion: 2, CorrelationID: "correlation-1",
	}
	job, err := domain.NewProcessingJob("job-1", source.BookID, source.SourceSHA256, bytesToHex(bytes.Repeat([]byte{2}, sha256.Size)), now)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := factory.ContentSelectionRequested(source, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if outbox.ID != "selection-request-1" || outbox.Type != ContentSelectionRequestedRoute {
		t.Fatalf("outbox = %#v", outbox)
	}
	var message ingestionv1.BookContentSelectionRequestedV1
	if err = proto.Unmarshal(outbox.Payload, &message); err != nil {
		t.Fatal(err)
	}
	expectedPolicyDigest := profile.PolicyDigest()
	if message.EventId != outbox.ID || message.RequestId != outbox.ID || message.BookId != source.BookID ||
		message.SourceReference != source.ObjectReference || message.SourceByteSize != source.ByteSize || message.LifecycleVersion != source.LifecycleVersion ||
		message.Mode != ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT || message.SelectorVersion != profile.PolicyVersion ||
		message.ParserProfile != profile.ParserVersion || message.IdempotencyKey != outbox.ID || message.CausationId != source.EventID || message.JobId != job.ID() || !bytes.Equal(message.PolicyDigest, expectedPolicyDigest[:]) {
		t.Fatalf("request = %#v", &message)
	}
}

func TestDecodeContentSelectionCompletedStrictlyMapsBoundedResult(t *testing.T) {
	message := validContentSelectionCompletedMessage()
	payload := mustMarshalMessage(t, message)
	result, err := DecodeContentSelectionCompleted(payload, 256)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventID != message.EventId || result.RequestID != message.RequestId || result.JobID != message.JobId ||
		result.PolicyVersion != message.SelectorVersion || result.ModelSHA256 != bytesToHex(message.ModelDigest) ||
		result.FallbackReason != "none" || result.FallbackUnfiltered || len(result.Ranges) != 1 ||
		result.Ranges[0].Reason != "title" || result.PayloadDigest != sha256.Sum256(payload) {
		t.Fatalf("result = %#v", result)
	}
}

func TestDecodeContentSelectionCompletedAllowsUnknownCountOnlyForFailOpen(t *testing.T) {
	message := validContentSelectionCompletedMessage()
	message.OriginalOrdinalCount = 0
	message.FallbackReason = ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT
	message.ExcludedRanges = nil
	result, err := DecodeContentSelectionCompleted(mustMarshalMessage(t, message), 256)
	if err != nil {
		t.Fatal(err)
	}
	if result.OriginalLocationCount != 0 || !result.FallbackUnfiltered || len(result.Ranges) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDecodeContentSelectionCompletedRejectsUnknownAndUnsafeFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ingestionv1.BookContentSelectionCompletedV1)
		wire   func([]byte) []byte
	}{
		{name: "zero original count", mutate: func(value *ingestionv1.BookContentSelectionCompletedV1) { value.OriginalOrdinalCount = 0 }},
		{name: "unknown mode", mutate: func(value *ingestionv1.BookContentSelectionCompletedV1) { value.Mode = 99 }},
		{name: "unknown reason", mutate: func(value *ingestionv1.BookContentSelectionCompletedV1) { value.ExcludedRanges[0].Reason = 99 }},
		{name: "digest length", mutate: func(value *ingestionv1.BookContentSelectionCompletedV1) { value.PolicyDigest = []byte{1} }},
		{name: "fallback with exclusions", mutate: func(value *ingestionv1.BookContentSelectionCompletedV1) {
			value.FallbackReason = ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT
		}},
		{name: "unknown wire field", wire: func(payload []byte) []byte {
			payload = protowire.AppendTag(payload, 99, protowire.VarintType)
			return protowire.AppendVarint(payload, 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := validContentSelectionCompletedMessage()
			if test.mutate != nil {
				test.mutate(message)
			}
			payload := mustMarshalMessage(t, message)
			if test.wire != nil {
				payload = test.wire(payload)
			}
			if _, err := DecodeContentSelectionCompleted(payload, 256); !errors.Is(err, application.ErrInvalidEvent) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func validContentSelectionCompletedMessage() *ingestionv1.BookContentSelectionCompletedV1 {
	return &ingestionv1.BookContentSelectionCompletedV1{
		EventId: "selection-result-1", RequestId: "selection-request-1", JobId: "job-1", BookId: "book-1",
		SourceSha256: bytes.Repeat([]byte{1}, sha256.Size), LifecycleVersion: 1,
		ProcessingProfileDigest: bytes.Repeat([]byte{2}, sha256.Size), OriginalOrdinalCount: 20,
		Mode: ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT, SelectorVersion: "layout-selector-v1",
		ParserVersion: "docling-serve-v1.21.0", ModelDigest: bytes.Repeat([]byte{3}, sha256.Size), PolicyDigest: bytes.Repeat([]byte{4}, sha256.Size),
		FallbackReason: ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE,
		ExcludedRanges: []*ingestionv1.ExcludedRangeV1{{StartOrdinal: 1, EndOrdinal: 2, Reason: ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE}},
		CorrelationId:  "correlation-1", OccurredAt: timestamppb.New(time.Now().UTC()), CausationId: "selection-request-1",
		Producer: "ingestion-layout-worker", SchemaVersion: "v1", IdempotencyKey: "selection-request-1", MediaType: application.MediaTypePDF,
	}
}

func mustMarshalMessage(t *testing.T, message proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func bytesToHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = digits[item>>4]
		result[index*2+1] = digits[item&0x0f]
	}
	return string(result)
}

func TestDecodeUploadedAcceptsFrozenCatalogContract(t *testing.T) {
	message := validUploadMessage()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeUploaded(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.BookID != message.BookId || !bytes.Equal(event.SourceSHA256[:], message.Sha256) {
		t.Fatalf("unexpected event: %#v", event)
	}
	if err = event.Validate(50 << 20); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeDeletionAndBuildSanitizedAcknowledgement(t *testing.T) {
	request := &catalogv1.BookDeletionRequestedV1{
		EventId:          "delete-event",
		BookId:           "book-1",
		CommandId:        "delete-command",
		LifecycleVersion: 2,
		ActorId:          "actor-1",
		CorrelationId:    "correlation-1",
		OccurredAt:       timestamppb.New(time.Now().UTC()),
		CausationId:      "delete-command",
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "delete-command",
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeDeletion(payload)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := NewProtoEventFactory(func() (string, error) { return "ack-event", nil })
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := factory.ArtifactsDeleted(event, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if outbox.Type != ArtifactsDeletedRoute {
		t.Fatalf("route = %q", outbox.Type)
	}
	var ack ingestionv1.BookArtifactsDeletedV1
	if err = proto.Unmarshal(outbox.Payload, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.BookId != event.BookID || ack.CommandId != event.CommandID ||
		ack.LifecycleVersion != event.LifecycleVersion ||
		ack.CausationId != event.EventID || ack.IdempotencyKey != event.CommandID {
		t.Fatalf("unexpected acknowledgment: %#v", &ack)
	}
}

func TestDecodeDeletionRejectsBookScopedIdempotency(t *testing.T) {
	request := &catalogv1.BookDeletionRequestedV1{
		EventId:          "delete-event",
		BookId:           "book-1",
		CommandId:        "delete-command",
		LifecycleVersion: 2,
		CorrelationId:    "correlation-1",
		OccurredAt:       timestamppb.New(time.Now().UTC()),
		CausationId:      "delete-command",
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "book-1",
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeDeletion(payload); !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeUploadedAcceptsAdditiveUnknownWireFields(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	payload = protowire.AppendTag(payload, 99, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	event, err := DecodeUploaded(payload)
	if err != nil {
		t.Fatalf("expected additive field compatibility, got %v", err)
	}
	if err = event.Validate(50 << 20); err != nil {
		t.Fatalf("known security fields remain valid: %v", err)
	}
}

func TestDecodeUploadedNormalizesLegacyLifecycleVersion(t *testing.T) {
	message := validUploadMessage()
	message.LifecycleVersion = 0
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeUploaded(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.LifecycleVersion != 1 {
		t.Fatalf("lifecycle version = %d, want 1", event.LifecycleVersion)
	}
	if err = event.Validate(50 << 20); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeUploadedRejectsNegativeLifecycleVersion(t *testing.T) {
	message := validUploadMessage()
	message.LifecycleVersion = -1
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeUploaded(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = event.Validate(50 << 20); !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("Validate() error = %v, want %v", err, application.ErrInvalidEvent)
	}
}

func validUploadMessage() *catalogv1.BookUploadedV1 {
	return &catalogv1.BookUploadedV1{
		EventId:          "event-1",
		BookId:           "book-1",
		ObjectReference:  "originals/01234567-89ab-cdef-0123-456789abcdef.pdf",
		Sha256:           bytes.Repeat([]byte{1}, 32),
		ByteSize:         1024,
		MediaType:        "application/pdf",
		CorrelationId:    "correlation-1",
		OccurredAt:       timestamppb.New(time.Now().UTC()),
		CausationId:      "correlation-1",
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "book-1",
		LifecycleVersion: 1,
	}
}
