package processing

import (
	"context"
	"errors"
	"testing"

	"github.com/rabbitmq/amqp091-go"
)

func TestHandleDeliveryRepublishesTransientFailureWithApplicationAttempt(t *testing.T) {
	acknowledger := &recordingAcknowledger{}
	publisher := &recordingRetryPublisher{}
	delivery := amqp091.Delivery{
		Acknowledger: acknowledger,
		DeliveryTag:  7,
		ContentType:  "application/x-protobuf",
		Type:         "ingestion.book.chunks-ready.v1",
		MessageId:    "event-1",
		Expiration:   "1",
		UserId:       "untrusted-publisher",
		ReplyTo:      "untrusted.reply.queue",
		Body:         []byte("payload"),
	}

	handleDelivery(context.Background(), "catalog.book-processing.v1", failingHandler{}, noopRecorder{}, publisher, delivery)

	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("acknowledgements = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
	if publisher.queue != "catalog.book-processing.v1" {
		t.Fatalf("retry queue = %q", publisher.queue)
	}
	if got := deliveryAttempt(publisher.message.Headers); got != 1 {
		t.Fatalf("application retry attempt = %d, want 1", got)
	}
	if len(publisher.message.Headers) != 1 {
		t.Fatalf("retry headers = %#v", publisher.message.Headers)
	}
	if string(publisher.message.Body) != "payload" || publisher.message.MessageId != "event-1" {
		t.Fatal("retry did not preserve the bounded event envelope")
	}
	if publisher.message.Expiration != "" || publisher.message.UserId != "" || publisher.message.ReplyTo != "" {
		t.Fatal("retry copied broker-sensitive expiration, identity, or reply routing")
	}
}

func TestHandleDeliveryDeadLettersAfterApplicationRetryLimit(t *testing.T) {
	acknowledger := &recordingAcknowledger{}
	publisher := &recordingRetryPublisher{}
	delivery := amqp091.Delivery{
		Acknowledger: acknowledger,
		DeliveryTag:  8,
		ContentType:  "application/x-protobuf",
		Type:         "retrieval.book.indexed.v1",
		MessageId:    "event-2",
		Body:         []byte("payload"),
		Headers:      amqp091.Table{applicationDeliveryCountHeader: int64(5)},
	}

	handleDelivery(context.Background(), "catalog.retrieval-terminal.v1", failingHandler{}, noopRecorder{}, publisher, delivery)

	if acknowledger.acks != 0 || acknowledger.nacks != 1 || acknowledger.requeue {
		t.Fatalf("acknowledgements = ack:%d nack:%d requeue:%t", acknowledger.acks, acknowledger.nacks, acknowledger.requeue)
	}
	if publisher.calls != 0 {
		t.Fatal("terminal retry was republished")
	}
}

func TestHandleDeliveryDeadLettersWhenRetryPublishFails(t *testing.T) {
	acknowledger := &recordingAcknowledger{}
	publisher := &recordingRetryPublisher{err: errors.New("publisher unavailable")}
	delivery := amqp091.Delivery{
		Acknowledger: acknowledger,
		DeliveryTag:  9,
		ContentType:  "application/x-protobuf",
		Body:         []byte("payload"),
	}

	handleDelivery(context.Background(), Queue, failingHandler{}, noopRecorder{}, publisher, delivery)

	if acknowledger.acks != 0 || acknowledger.nacks != 1 || acknowledger.requeue {
		t.Fatalf("acknowledgements = ack:%d nack:%d requeue:%t", acknowledger.acks, acknowledger.nacks, acknowledger.requeue)
	}
	if publisher.calls != 1 {
		t.Fatalf("retry publish calls = %d, want 1", publisher.calls)
	}
}

func TestHandleDeliveryLeavesCanceledDeliveryUnsettled(t *testing.T) {
	acknowledger := &recordingAcknowledger{}
	publisher := &recordingRetryPublisher{}
	delivery := amqp091.Delivery{
		Acknowledger: acknowledger,
		DeliveryTag:  10,
		ContentType:  "application/x-protobuf",
		Body:         []byte("payload"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handleDelivery(ctx, Queue, failingHandler{}, noopRecorder{}, publisher, delivery)

	if acknowledger.acks != 0 || acknowledger.nacks != 0 {
		t.Fatalf("canceled delivery was settled: ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
	if publisher.calls != 0 {
		t.Fatalf("retry publish calls = %d, want 0", publisher.calls)
	}
}

func TestDeliveryAttemptFallsBackToQuorumHeaderAndFailsClosed(t *testing.T) {
	if got := deliveryAttempt(amqp091.Table{"x-delivery-count": int64(3)}); got != 3 {
		t.Fatalf("quorum delivery count = %d", got)
	}
	if got := deliveryAttempt(amqp091.Table{applicationDeliveryCountHeader: "bad"}); got != 5 {
		t.Fatalf("malformed application delivery count = %d", got)
	}
}

type failingHandler struct{}

func (failingHandler) HandleEnvelope(context.Context, string, string, []byte) (bool, error) {
	return false, errors.New("temporary storage failure")
}

type noopRecorder struct{}

func (noopRecorder) ProcessingConsumerUnavailable() {}
func (noopRecorder) ProcessingEventRejected()       {}
func (noopRecorder) ProcessingEventConflict()       {}
func (noopRecorder) ProcessingEventApplyFailed()    {}

type recordingRetryPublisher struct {
	calls   int
	queue   string
	message amqp091.Publishing
	err     error
}

func (p *recordingRetryPublisher) PublishRetry(_ context.Context, queue string, message amqp091.Publishing) error {
	p.calls++
	p.queue = queue
	p.message = message
	return p.err
}

type recordingAcknowledger struct {
	acks    int
	nacks   int
	requeue bool
}

func (a *recordingAcknowledger) Ack(uint64, bool) error {
	a.acks++
	return nil
}

func (a *recordingAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.nacks++
	a.requeue = requeue
	return nil
}

func (a *recordingAcknowledger) Reject(_ uint64, requeue bool) error {
	a.nacks++
	a.requeue = requeue
	return nil
}
