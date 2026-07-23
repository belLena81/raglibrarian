package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingAcknowledger struct {
	acked    bool
	nacked   bool
	rejected bool
	requeued bool
}

func (a *recordingAcknowledger) Ack(uint64, bool) error {
	a.acked = true
	return nil
}
func (a *recordingAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.nacked = true
	a.requeued = requeue
	return nil
}

type recordingPublisher struct {
	exchange  string
	key       string
	mandatory bool
	immediate bool
	message   amqp091.Publishing
	err       error
	cancel    context.CancelFunc
}

func (p *recordingPublisher) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, message amqp091.Publishing) error {
	p.exchange = exchange
	p.key = key
	p.mandatory = mandatory
	p.immediate = immediate
	p.message = message
	if p.cancel != nil {
		p.cancel()
	}
	return p.err
}
func (a *recordingAcknowledger) Reject(_ uint64, requeue bool) error {
	a.rejected = true
	a.requeued = requeue
	return nil
}

type countingProcessor struct {
	calls int
	err   error
}

func (p *countingProcessor) Process(context.Context, application.UploadedEvent) error {
	p.calls++
	return p.err
}

func (p *countingProcessor) ProcessDeletion(context.Context, application.DeletionEvent) error {
	p.calls++
	return p.err
}

func TestConsumerRejectsMissingOrMismatchedMessageID(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"", "different-event"} {
		t.Run(messageID, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{}
			consumer := &Consumer{processor: processor, now: time.Now}
			consumer.handle(context.Background(), amqp091.Delivery{Acknowledger: acknowledger, ContentType: "application/x-protobuf", Type: UploadRoute, MessageId: messageID, Body: payload})
			if !acknowledger.rejected || acknowledger.requeued || processor.calls != 0 {
				t.Fatalf("invalid identity must be rejected without processing: %#v calls=%d", acknowledger, processor.calls)
			}
		})
	}
}

func TestConsumerAppliesSharedDeliveryDisposition(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		processErr error
		wantAck    bool
		wantReject bool
		wantNack   bool
	}{
		{name: "success", wantAck: true},
		{name: "durable retry", processErr: application.ErrProcessingDeferred, wantAck: true},
		{name: "invalid", processErr: application.ErrInvalidEvent, wantReject: true},
		{name: "conflict", processErr: application.ErrConflictingEvent, wantReject: true},
		{name: "unsupported", processErr: application.ErrUnsupportedProcessingProfile, wantReject: true},
		{name: "transient", processErr: errors.New("dependency unavailable"), wantAck: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{err: test.processErr}
			consumer := &Consumer{processor: processor, publisher: &recordingPublisher{}, now: time.Now}
			consumer.handle(context.Background(), amqp091.Delivery{
				Acknowledger: acknowledger,
				ContentType:  "application/x-protobuf",
				Type:         UploadRoute,
				MessageId:    validUploadMessage().EventId,
				Body:         payload,
			})
			if acknowledger.acked != test.wantAck || acknowledger.nacked != test.wantNack || acknowledger.rejected != test.wantReject || acknowledger.requeued || processor.calls != 1 {
				t.Fatalf("delivery result = %#v calls=%d", acknowledger, processor.calls)
			}
		})
	}
}

func TestConsumerAppliesDispositionAfterShutdownCancellation(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		processErr    error
		wantAck       bool
		wantUnsettled bool
	}{
		{name: "durable retry", processErr: application.ErrProcessingDeferred, wantAck: true},
		{name: "non-durable transient", processErr: errors.New("database unavailable"), wantUnsettled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{err: test.processErr}
			consumer := &Consumer{processor: processor, publisher: &recordingPublisher{}, now: time.Now}

			consumer.handle(ctx, amqp091.Delivery{
				Acknowledger: acknowledger,
				ContentType:  "application/x-protobuf",
				Type:         UploadRoute,
				MessageId:    validUploadMessage().EventId,
				Body:         payload,
			})

			if acknowledger.acked != test.wantAck || acknowledger.nacked || acknowledger.rejected || acknowledger.requeued || processor.calls != 1 {
				t.Fatalf("shutdown disposition result = %#v calls=%d", acknowledger, processor.calls)
			}
			if test.wantUnsettled && (acknowledger.acked || acknowledger.nacked || acknowledger.rejected) {
				t.Fatal("canceled transient delivery must remain unsettled")
			}
		})
	}
}

func TestConsumerRepublishesTransientDeliveryWithBoundedSanitizedRetry(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	tests := []struct {
		name        string
		eventType   string
		payload     []byte
		messageID   string
		headers     amqp091.Table
		wantKey     string
		wantAttempt int64
	}{
		{name: "upload first retry", eventType: UploadRoute, payload: mustMarshal(t, validUploadMessage()), messageID: validUploadMessage().EventId, wantKey: "ingestion.retry.5s", wantAttempt: 1},
		{name: "upload second retry", eventType: UploadRoute, payload: mustMarshal(t, validUploadMessage()), messageID: validUploadMessage().EventId, headers: amqp091.Table{applicationDeliveryCountHeader: int32(1)}, wantKey: "ingestion.retry.30s", wantAttempt: 2},
		{name: "upload third retry from broker count", eventType: UploadRoute, payload: mustMarshal(t, validUploadMessage()), messageID: validUploadMessage().EventId, headers: amqp091.Table{"x-delivery-count": int64(2)}, wantKey: "ingestion.retry.30s", wantAttempt: 3},
		{name: "upload fourth retry", eventType: UploadRoute, payload: mustMarshal(t, validUploadMessage()), messageID: validUploadMessage().EventId, headers: amqp091.Table{applicationDeliveryCountHeader: int64(3)}, wantKey: "ingestion.retry.2m", wantAttempt: 4},
		{name: "upload fifth retry", eventType: UploadRoute, payload: mustMarshal(t, validUploadMessage()), messageID: validUploadMessage().EventId, headers: amqp091.Table{applicationDeliveryCountHeader: int32(4)}, wantKey: "ingestion.retry.2m", wantAttempt: 5},
		{name: "deletion first retry", eventType: DeletionRoute, payload: mustMarshal(t, validDeletionMessage()), messageID: validDeletionMessage().EventId, wantKey: "ingestion.deletion.retry.5s", wantAttempt: 1},
		{name: "deletion second retry", eventType: DeletionRoute, payload: mustMarshal(t, validDeletionMessage()), messageID: validDeletionMessage().EventId, headers: amqp091.Table{applicationDeliveryCountHeader: int32(1)}, wantKey: "ingestion.deletion.retry.30s", wantAttempt: 2},
		{name: "deletion fourth retry", eventType: DeletionRoute, payload: mustMarshal(t, validDeletionMessage()), messageID: validDeletionMessage().EventId, headers: amqp091.Table{applicationDeliveryCountHeader: int64(3)}, wantKey: "ingestion.deletion.retry.2m", wantAttempt: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			publisher := &recordingPublisher{}
			consumer := &Consumer{processor: &countingProcessor{err: errors.New("temporary")}, publisher: publisher, now: func() time.Time { return now }}
			headers := amqp091.Table{
				"CC":          []any{"untrusted.route"},
				"x-death":     []any{"untrusted"},
				"user-header": "untrusted",
			}
			for key, value := range test.headers {
				headers[key] = value
			}
			consumer.handle(context.Background(), amqp091.Delivery{
				Acknowledger: acknowledger,
				Headers:      headers,
				ContentType:  "application/x-protobuf",
				Type:         test.eventType,
				MessageId:    test.messageID,
				UserId:       "untrusted",
				ReplyTo:      "untrusted",
				Expiration:   "1",
				Body:         test.payload,
			})
			if !acknowledger.acked || acknowledger.nacked || publisher.exchange != RetryExchange || publisher.key != test.wantKey || !publisher.mandatory || publisher.immediate {
				t.Fatalf("retry result ack=%#v publish=%#v", acknowledger, publisher)
			}
			if publisher.message.DeliveryMode != amqp091.Persistent || publisher.message.ContentType != "application/x-protobuf" || publisher.message.MessageId != test.messageID || publisher.message.Type != test.eventType || !publisher.message.Timestamp.Equal(now.UTC()) {
				t.Fatalf("unexpected retry envelope: %#v", publisher.message)
			}
			if len(publisher.message.Headers) != 1 || publisher.message.Headers[applicationDeliveryCountHeader] != test.wantAttempt {
				t.Fatalf("retry headers were not sanitized: %#v", publisher.message.Headers)
			}
			if publisher.message.UserId != "" || publisher.message.ReplyTo != "" || publisher.message.Expiration != "" {
				t.Fatalf("untrusted properties propagated: %#v", publisher.message)
			}
		})
	}
}

func mustMarshal(t *testing.T, message proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func validDeletionMessage() *catalogv1.BookDeletionRequestedV1 {
	return &catalogv1.BookDeletionRequestedV1{
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
}

func TestConsumerDeadLettersExhaustedOrInvalidRetryCounts(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		headers amqp091.Table
	}{
		{name: "exhausted", headers: amqp091.Table{applicationDeliveryCountHeader: int32(5)}},
		{name: "negative", headers: amqp091.Table{applicationDeliveryCountHeader: int64(-1)}},
		{name: "oversized", headers: amqp091.Table{applicationDeliveryCountHeader: int64(6)}},
		{name: "malformed", headers: amqp091.Table{applicationDeliveryCountHeader: "1"}},
		{name: "application header takes precedence", headers: amqp091.Table{applicationDeliveryCountHeader: "bad", "x-delivery-count": int64(0)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			publisher := &recordingPublisher{}
			consumer := &Consumer{processor: &countingProcessor{err: errors.New("temporary")}, publisher: publisher, now: time.Now}
			consumer.handle(context.Background(), amqp091.Delivery{Acknowledger: acknowledger, Headers: test.headers, ContentType: "application/x-protobuf", Type: UploadRoute, MessageId: validUploadMessage().EventId, Body: payload})
			if !acknowledger.nacked || acknowledger.requeued || acknowledger.acked || publisher.exchange != "" {
				t.Fatalf("unsafe count must dead-letter without publishing: ack=%#v publisher=%#v", acknowledger, publisher)
			}
		})
	}
}

func TestConsumerDeadLettersWhenRetryPublishFailsWhileActive(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	acknowledger := &recordingAcknowledger{}
	consumer := &Consumer{processor: &countingProcessor{err: errors.New("temporary")}, publisher: &recordingPublisher{err: errors.New("publish unavailable")}, now: time.Now}
	consumer.handle(context.Background(), amqp091.Delivery{Acknowledger: acknowledger, ContentType: "application/x-protobuf", Type: UploadRoute, MessageId: validUploadMessage().EventId, Body: payload})
	if !acknowledger.nacked || acknowledger.requeued || acknowledger.acked {
		t.Fatalf("failed retry publish must dead-letter: %#v", acknowledger)
	}
}

func TestConsumerLeavesDeliveryUnsettledWhenContextCancelsDuringRetryPublish(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	acknowledger := &recordingAcknowledger{}
	consumer := &Consumer{processor: &countingProcessor{err: errors.New("temporary")}, publisher: &recordingPublisher{err: context.Canceled, cancel: cancel}, now: time.Now}
	consumer.handle(ctx, amqp091.Delivery{Acknowledger: acknowledger, ContentType: "application/x-protobuf", Type: UploadRoute, MessageId: validUploadMessage().EventId, Body: payload})
	if acknowledger.nacked || acknowledger.requeued || acknowledger.acked || acknowledger.rejected {
		t.Fatalf("canceled delivery must remain unsettled: %#v", acknowledger)
	}
}

func TestNewConsumerRejectsMissingRetryPublisher(t *testing.T) {
	consumer, err := NewConsumer(&amqp091.Channel{}, "ingestion", 1, &countingProcessor{}, nil)
	if err == nil || consumer != nil {
		t.Fatal("missing retry publisher must be rejected before channel setup")
	}
}
