package outbox

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rabbitmq/amqp091-go"

	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

func TestPublishPendingRetriesBrokerFailureWithoutChangingEvent(t *testing.T) {
	event := repository.PendingOutboxEvent{ID: "event-1", Type: "catalog.book.uploaded.v1", Payload: []byte("payload"), Attempts: 2}
	store := &fakeStore{claims: [][]repository.PendingOutboxEvent{{event}, {event}}}
	publisher := &fakePublisher{errors: []error{errors.New("broker unavailable"), nil}}
	recorder := &fakeRecorder{}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	publishPending(context.Background(), store, publisher, recorder, now)
	publishPending(context.Background(), store, publisher, recorder, now.Add(time.Second))

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

	publishPending(context.Background(), store, publisher, recorder, now)
	publishPending(context.Background(), store, publisher, recorder, now.Add(time.Second))

	assertStablePublications(t, publisher.publications, event)
	if len(store.marked) != 2 || store.marked[0] != event.ID || store.marked[1] != event.ID {
		t.Fatalf("marked = %#v, want [%q %q]", store.marked, event.ID, event.ID)
	}
	if recorder.markFailed != 1 {
		t.Fatalf("mark failures = %d, want 1", recorder.markFailed)
	}
}

func TestPublicationRouteSeparatesDurableWorkFromDisposableStatus(t *testing.T) {
	exchange, key, mandatory, err := publicationRoute("catalog.book.uploaded.v1")
	if err != nil || exchange != uploadExchange || key != "catalog.book.uploaded.v1" || !mandatory {
		t.Fatalf("upload route = %q %q %v %v", exchange, key, mandatory, err)
	}
	exchange, key, mandatory, err = publicationRoute("catalog.book.processing-status-changed.v1")
	if err != nil || exchange != statusExchange || key != "catalog.book.processing-status-changed.v1" || mandatory {
		t.Fatalf("status route = %q %q %v %v", exchange, key, mandatory, err)
	}
	if _, _, _, err = publicationRoute("catalog.unknown.v1"); err == nil {
		t.Fatal("expected unknown event rejection")
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
	retries    []string
	markErrors []error
	markIndex  int
	marked     []string
}

func (s *fakeStore) ClaimOutbox(context.Context, time.Time, time.Duration) ([]repository.PendingOutboxEvent, error) {
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
