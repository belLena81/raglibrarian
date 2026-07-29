package outbox

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/rabbitmq/amqp091-go"

	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

func TestPublishPendingRetriesBrokerFailureWithoutChangingEvent(t *testing.T) {
	event := repository.PendingOutboxEvent{ID: "event-1", Type: "catalog.book.uploaded.v1", Payload: []byte("payload"), Attempts: 2}
	store := &fakeStore{claims: [][]repository.PendingOutboxEvent{{event}, {event}}}
	publisher := &fakePublisher{errors: []error{errors.New("broker unavailable"), nil}}
	recorder := &fakeRecorder{}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	policy := Policy{PollInterval: time.Second, DrainBudget: 250 * time.Millisecond, Lease: 30 * time.Second, PublishTimeout: 5 * time.Second}
	publishPending(context.Background(), store, publisher, recorder, now, policy)
	publishPending(context.Background(), store, publisher, recorder, now.Add(time.Second), policy)

	assertStablePublications(t, publisher.publications, event)
	if len(store.retries) != 1 || store.retries[0] != event.ID {
		t.Fatalf("retries = %#v, want [%q]", store.retries, event.ID)
	}
	if len(store.marked) != 1 || store.marked[0] != event.ID {
		t.Fatalf("marked = %#v, want [%q]", store.marked, event.ID)
	}
	if recorder.publishFailed != 1 {
		t.Fatalf("publish failures = %d, want 1", recorder.publishFailed)
	}
}

func TestPublishPendingReplaysStableEventAfterMarkFailure(t *testing.T) {
	event := repository.PendingOutboxEvent{ID: "event-2", Type: "catalog.book.uploaded.v1", Payload: []byte("payload"), Attempts: 0}
	store := &fakeStore{
		claims:     [][]repository.PendingOutboxEvent{{event}, {event}},
		markErrors: []error{errors.New("database unavailable"), nil},
	}
	publisher := &fakePublisher{}
	recorder := &fakeRecorder{}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	policy := Policy{PollInterval: time.Second, DrainBudget: 250 * time.Millisecond, Lease: 30 * time.Second, PublishTimeout: 5 * time.Second}
	publishPending(context.Background(), store, publisher, recorder, now, policy)
	publishPending(context.Background(), store, publisher, recorder, now.Add(time.Second), policy)

	assertStablePublications(t, publisher.publications, event)
	if len(store.marked) != 2 || store.marked[0] != event.ID || store.marked[1] != event.ID {
		t.Fatalf("marked = %#v, want [%q %q]", store.marked, event.ID, event.ID)
	}
	if recorder.markFailed != 1 {
		t.Fatalf("mark failures = %d, want 1", recorder.markFailed)
	}
}

func TestDrainPendingClaimsUntilStoreIsEmpty(t *testing.T) {
	events := []repository.PendingOutboxEvent{
		{ID: "event-1", Type: "catalog.book.uploaded.v1", Payload: []byte("first")},
		{ID: "event-2", Type: "catalog.book.processing-status-changed.v1", Payload: []byte("second")},
	}
	store := &fakeStore{claims: [][]repository.PendingOutboxEvent{events, nil}}
	publisher := &fakePublisher{}

	drainPending(context.Background(), store, publisher, &fakeRecorder{}, time.Now(), Policy{PollInterval: time.Second, DrainBudget: time.Second, Lease: 30 * time.Second, PublishTimeout: 5 * time.Second})

	if store.claimIndex != 2 {
		t.Fatalf("claims = %d, want 2", store.claimIndex)
	}
	if len(store.marked) != 2 || len(publisher.publications) != 2 {
		t.Fatalf("marked = %d, publications = %d, want 2 each", len(store.marked), len(publisher.publications))
	}
}

func TestDrainPendingBoundsBlockingStoreOperation(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPending(context.Background(), blockingStore{}, &fakePublisher{}, &fakeRecorder{}, time.Now(), Policy{
			PollInterval:   time.Second,
			DrainBudget:    20 * time.Millisecond,
			Lease:          30 * time.Second,
			PublishTimeout: 5 * time.Second,
		})
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("drain did not cancel a blocking store operation")
	}
}

func TestPublicationRouteSeparatesDurableWorkFromDisposableStatus(t *testing.T) {
	exchange, key, mandatory, err := publicationRoute("catalog.book.uploaded.v1")
	if err != nil || exchange != contracts.ExchangeEvents || key != "catalog.book.uploaded.v1" || !mandatory {
		t.Fatalf("upload route = %q %q %v %v", exchange, key, mandatory, err)
	}
	exchange, key, mandatory, err = publicationRoute("catalog.book.processing-status-changed.v1")
	if err != nil || exchange != contracts.ExchangeEdgeStatus || key != "catalog.book.processing-status-changed.v1" || mandatory {
		t.Fatalf("status route = %q %q %v %v", exchange, key, mandatory, err)
	}
	for _, eventType := range []string{
		"catalog.book.reindex-requested.v1",
		"catalog.book.deletion-requested.v1",
	} {
		exchange, key, mandatory, err = publicationRoute(eventType)
		if err != nil || exchange != contracts.ExchangeEvents || key != eventType || !mandatory {
			t.Fatalf("%s route = %q %q %v %v", eventType, exchange, key, mandatory, err)
		}
	}
	if _, _, _, err = publicationRoute("catalog.unknown.v1"); err == nil {
		t.Fatal("expected unknown event rejection")
	}
}

func TestPublishPendingUsesConfiguredLease(t *testing.T) {
	event := repository.PendingOutboxEvent{ID: "event-1", Type: "catalog.book.uploaded.v1", Payload: []byte("payload")}
	store := &fakeStore{claims: [][]repository.PendingOutboxEvent{{event}}}
	publisher := &fakePublisher{}
	policy := Policy{Lease: 45 * time.Second}

	publishPending(context.Background(), store, publisher, &fakeRecorder{}, time.Now(), policy)

	if len(store.leases) != 1 || store.leases[0] != 45*time.Second {
		t.Fatalf("leases = %#v", store.leases)
	}
}

func assertStablePublications(t *testing.T, publications []amqp091.Publishing, event repository.PendingOutboxEvent) {
	t.Helper()
	if len(publications) != 2 {
		t.Fatalf("publications = %d, want 2", len(publications))
	}
	for index, publication := range publications {
		if publication.MessageId != event.ID || publication.Type != event.Type || !bytes.Equal(publication.Body, event.Payload) {
			t.Fatalf("publication %d = {id:%q type:%q body:%q}", index, publication.MessageId, publication.Type, publication.Body)
		}
	}
}

type fakeStore struct {
	claims     [][]repository.PendingOutboxEvent
	claimIndex int
	leases     []time.Duration
	retries    []string
	markErrors []error
	markIndex  int
	marked     []string
}

type blockingStore struct{}

func (blockingStore) ClaimOutbox(ctx context.Context, _ time.Time, _ time.Duration) ([]repository.PendingOutboxEvent, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingStore) MarkPublished(context.Context, string, time.Time) error {
	return nil
}

func (blockingStore) RetryOutbox(context.Context, string, time.Time, int) error {
	return nil
}

func (s *fakeStore) ClaimOutbox(_ context.Context, _ time.Time, lease time.Duration) ([]repository.PendingOutboxEvent, error) {
	s.leases = append(s.leases, lease)
	if s.claimIndex >= len(s.claims) {
		return nil, nil
	}
	events := s.claims[s.claimIndex]
	s.claimIndex++
	return events, nil
}

func (s *fakeStore) MarkPublished(_ context.Context, id string, _ time.Time) error {
	s.marked = append(s.marked, id)
	if s.markIndex >= len(s.markErrors) {
		return nil
	}
	err := s.markErrors[s.markIndex]
	s.markIndex++
	return err
}

func (s *fakeStore) RetryOutbox(_ context.Context, id string, _ time.Time, _ int) error {
	s.retries = append(s.retries, id)
	return nil
}

type fakePublisher struct {
	publications []amqp091.Publishing
	errors       []error
}

func (p *fakePublisher) PublishWithContext(_ context.Context, _, _ string, _, _ bool, publication amqp091.Publishing) error {
	p.publications = append(p.publications, publication)
	index := len(p.publications) - 1
	if index < len(p.errors) {
		return p.errors[index]
	}
	return nil
}

type fakeRecorder struct {
	publishFailed int
	markFailed    int
}

func (*fakeRecorder) OutboxClaimFailed()     {}
func (r *fakeRecorder) OutboxPublishFailed() { r.publishFailed++ }
func (*fakeRecorder) OutboxRetryFailed()     {}
func (r *fakeRecorder) OutboxMarkFailed()    { r.markFailed++ }
