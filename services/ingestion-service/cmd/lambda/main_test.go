package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type stubProcessor struct {
	err   error
	calls int
}

func (s *stubProcessor) Process(context.Context, application.UploadedEvent) error {
	s.calls++
	return s.err
}

type stubPublisher struct {
	err   error
	calls int
}

func (s *stubPublisher) PublishPending(context.Context) error {
	s.calls++
	return s.err
}

func TestHandleWithLoaderReturnsBootstrapFailure(t *testing.T) {
	want := errors.New("bootstrap failed")
	loads := 0
	err := handleWithLoader(context.Background(), validIncoming(t), func(context.Context) (eventProcessor, pendingPublisher, error) {
		loads++
		return nil, nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("handleWithLoader() error = %v, want %v", err, want)
	}
	if loads != 1 {
		t.Fatalf("loader calls = %d, want 1", loads)
	}
}

func TestHandleInvocationRejectsInvalidBatchShape(t *testing.T) {
	tests := []struct {
		name     string
		incoming func(*testing.T) events.RabbitMQEvent
	}{
		{name: "source", incoming: func(t *testing.T) events.RabbitMQEvent {
			incoming := validIncoming(t)
			incoming.EventSource = "not-rmq"
			return incoming
		}},
		{name: "no queues", incoming: func(*testing.T) events.RabbitMQEvent {
			return events.RabbitMQEvent{EventSource: "aws:rmq"}
		}},
		{name: "multiple queues", incoming: func(t *testing.T) events.RabbitMQEvent {
			incoming := validIncoming(t)
			incoming.MessagesByQueue["other"] = incoming.MessagesByQueue["queue"]
			return incoming
		}},
		{name: "multiple messages", incoming: func(t *testing.T) events.RabbitMQEvent {
			incoming := validIncoming(t)
			incoming.MessagesByQueue["queue"] = append(incoming.MessagesByQueue["queue"], incoming.MessagesByQueue["queue"][0])
			return incoming
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &stubProcessor{}
			publisher := &stubPublisher{}
			loads := 0
			err := handleWithLoader(context.Background(), test.incoming(t), func(context.Context) (eventProcessor, pendingPublisher, error) {
				loads++
				return processor, publisher, nil
			})
			if err == nil {
				t.Fatal("handleWithLoader() error = nil, want invalid batch error")
			}
			if loads != 0 || processor.calls != 0 || publisher.calls != 0 {
				t.Fatalf("unexpected calls: load=%d process=%d publish=%d", loads, processor.calls, publisher.calls)
			}
		})
	}
}

func TestHandleInvocationAcknowledgesMalformedMessagesWithoutProcessing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*events.RabbitMQMessage)
	}{
		{name: "metadata", mutate: func(message *events.RabbitMQMessage) { message.BasicProperties.ContentType = "text/plain" }},
		{name: "body size", mutate: func(message *events.RabbitMQMessage) { message.BasicProperties.BodySize = 256<<10 + 1 }},
		{name: "empty body", mutate: func(message *events.RabbitMQMessage) { message.Data = "" }},
		{name: "base64", mutate: func(message *events.RabbitMQMessage) { message.Data = "%%%" }},
		{name: "protobuf", mutate: func(message *events.RabbitMQMessage) { message.Data = base64.StdEncoding.EncodeToString([]byte{0xff}) }},
		{name: "message ID", mutate: func(message *events.RabbitMQMessage) { wrong := "event-2"; message.BasicProperties.MessageID = &wrong }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incoming := validIncoming(t)
			message := incoming.MessagesByQueue["queue"][0]
			test.mutate(&message)
			incoming.MessagesByQueue["queue"] = []events.RabbitMQMessage{message}
			processor := &stubProcessor{}
			publisher := &stubPublisher{}
			loads := 0
			err := handleWithLoader(context.Background(), incoming, func(context.Context) (eventProcessor, pendingPublisher, error) {
				loads++
				return processor, publisher, errors.New("bootstrap unavailable")
			})
			if err != nil {
				t.Fatalf("handleWithLoader() error = %v, want nil", err)
			}
			if loads != 0 || processor.calls != 0 || publisher.calls != 0 {
				t.Fatalf("unexpected calls: load=%d process=%d publish=%d", loads, processor.calls, publisher.calls)
			}
		})
	}
}

func TestHandleInvocationAppliesDeliveryDispositionAfterPublishing(t *testing.T) {
	transient := errors.New("database unavailable")
	tests := []struct {
		name       string
		processErr error
		wantErr    error
	}{
		{name: "success"},
		{name: "deferred", processErr: application.ErrProcessingDeferred},
		{name: "invalid", processErr: application.ErrInvalidEvent},
		{name: "conflicting", processErr: application.ErrConflictingEvent},
		{name: "unsupported profile", processErr: application.ErrUnsupportedProcessingProfile},
		{name: "transient", processErr: transient, wantErr: transient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &stubProcessor{err: test.processErr}
			publisher := &stubPublisher{}
			loads := 0
			err := handleWithLoader(context.Background(), validIncoming(t), func(context.Context) (eventProcessor, pendingPublisher, error) {
				loads++
				return processor, publisher, nil
			})
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("handleWithLoader() error = %v, want %v", err, test.wantErr)
			}
			if loads != 1 || processor.calls != 1 || publisher.calls != 1 {
				t.Fatalf("calls: load=%d process=%d publish=%d, want 1 each", loads, processor.calls, publisher.calls)
			}
		})
	}
}

func TestHandleInvocationReturnsPublicationFailureForEveryProcessOutcome(t *testing.T) {
	publishErr := errors.New("outbox unavailable")
	processErrors := []error{
		nil,
		application.ErrProcessingDeferred,
		application.ErrInvalidEvent,
		application.ErrConflictingEvent,
		application.ErrUnsupportedProcessingProfile,
		errors.New("database unavailable"),
	}
	for _, processErr := range processErrors {
		processor := &stubProcessor{err: processErr}
		publisher := &stubPublisher{err: publishErr}
		loads := 0
		err := handleWithLoader(context.Background(), validIncoming(t), func(context.Context) (eventProcessor, pendingPublisher, error) {
			loads++
			return processor, publisher, nil
		})
		if !errors.Is(err, publishErr) {
			t.Fatalf("handleWithLoader() error = %v, want publication failure", err)
		}
		if processErr != nil && !errors.Is(err, processErr) {
			t.Fatalf("handleWithLoader() error = %v, want joined process error %v", err, processErr)
		}
		if loads != 1 || processor.calls != 1 || publisher.calls != 1 {
			t.Fatalf("calls: load=%d process=%d publish=%d, want 1 each", loads, processor.calls, publisher.calls)
		}
	}
}

func TestMessageIDMatchesRequiresExactNonEmptyIdentity(t *testing.T) {
	empty := ""
	wrong := "event-2"
	exact := "event-1"
	tests := []struct {
		name      string
		messageID *string
		want      bool
	}{
		{name: "nil", messageID: nil, want: false},
		{name: "empty", messageID: &empty, want: false},
		{name: "different", messageID: &wrong, want: false},
		{name: "exact", messageID: &exact, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := messageIDMatches(test.messageID, "event-1"); got != test.want {
				t.Fatalf("messageIDMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func validIncoming(t *testing.T) events.RabbitMQEvent {
	t.Helper()
	message := &catalogv1.BookUploadedV1{
		EventId:         "event-1",
		BookId:          "book-1",
		ObjectReference: "originals/01234567-89ab-cdef-0123-456789abcdef.pdf",
		Sha256:          bytes.Repeat([]byte{1}, 32),
		ByteSize:        1024,
		MediaType:       "application/pdf",
		CorrelationId:   "correlation-1",
		OccurredAt:      timestamppb.New(time.Now().UTC()),
		CausationId:     "correlation-1",
		Producer:        "catalog-service",
		SchemaVersion:   "v1",
		IdempotencyKey:  "book-1",
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	route := transport.UploadRoute
	messageID := message.EventId
	return events.RabbitMQEvent{
		EventSource: "aws:rmq",
		MessagesByQueue: map[string][]events.RabbitMQMessage{
			"queue": {{
				BasicProperties: events.RabbitMQBasicProperties{
					ContentType: "application/x-protobuf",
					Type:        &route,
					MessageID:   &messageID,
					BodySize:    uint64(len(payload)),
				},
				Data: base64.StdEncoding.EncodeToString(payload),
			}},
		},
	}
}
