package publisher_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/metadata/publisher"
)

// ── fakePublisher ─────────────────────────────────────────────────────────────
// In-memory BookPublisher used in use-case and gRPC tests.
// Exported so sibling packages can embed it without re-declaring.

// FakePublisher records every published event for assertion in tests.
// Safe for concurrent use.
type FakePublisher struct {
	mu     sync.Mutex
	events []publisher.BookEvent
	err    error // if non-nil, Publish returns this error
}

// NewFakePublisher constructs a FakePublisher with an optional forced error.
func NewFakePublisher(forceErr error) *FakePublisher {
	return &FakePublisher{err: forceErr}
}

func (f *FakePublisher) Publish(_ context.Context, event publisher.BookEvent) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *FakePublisher) Close() error { return nil }

// Events returns a snapshot of all recorded events.
func (f *FakePublisher) Events() []publisher.BookEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]publisher.BookEvent, len(f.events))
	copy(out, f.events)
	return out
}

// Compile-time: FakePublisher satisfies BookPublisher.
var _ publisher.BookPublisher = (*FakePublisher)(nil)

// ── Port contract tests ───────────────────────────────────────────────────────
// These tests verify the BookEvent type and EventType constants are correct,
// independent of any broker implementation.

func TestBookEvent_JSONRoundTrip(t *testing.T) {
	evt := publisher.BookEvent{
		Event:      publisher.EventBookCreated,
		BookID:     "b-1",
		S3Key:      "books/b-1/file.pdf",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	var got publisher.BookEvent
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, evt.Event, got.Event)
	assert.Equal(t, evt.BookID, got.BookID)
	assert.Equal(t, evt.S3Key, got.S3Key)
	assert.Equal(t, evt.OccurredAt, got.OccurredAt)
}

func TestBookEvent_S3Key_OmittedWhenEmpty(t *testing.T) {
	// s3_key is omitempty — must not appear in the JSON when blank so the
	// Lambda can distinguish "not yet uploaded" from "key is empty string".
	evt := publisher.BookEvent{
		Event:      publisher.EventBookCreated,
		BookID:     "b-1",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "s3_key")
}

func TestEventType_Constants(t *testing.T) {
	// Routing key strings are load-bearing — consumer queue bindings depend on them.
	assert.Equal(t, publisher.EventType("book.created"), publisher.EventBookCreated)
	assert.Equal(t, publisher.EventType("book.reindex_requested"), publisher.EventBookReindexRequested)
}

// ── FakePublisher behaviour tests ─────────────────────────────────────────────

func TestFakePublisher_RecordsEvents(t *testing.T) {
	fp := NewFakePublisher(nil)
	evt := publisher.BookEvent{Event: publisher.EventBookCreated, BookID: "b-1"}

	require.NoError(t, fp.Publish(context.Background(), evt))

	events := fp.Events()
	require.Len(t, events, 1)
	assert.Equal(t, publisher.EventBookCreated, events[0].Event)
	assert.Equal(t, "b-1", events[0].BookID)
}

func TestFakePublisher_ForcedError_Propagates(t *testing.T) {
	fp := NewFakePublisher(assert.AnError)

	err := fp.Publish(context.Background(), publisher.BookEvent{Event: publisher.EventBookCreated})

	assert.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, fp.Events())
}

func TestFakePublisher_ConcurrentPublish_Safe(t *testing.T) {
	fp := NewFakePublisher(nil)
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_ = fp.Publish(context.Background(), publisher.BookEvent{
				Event:  publisher.EventBookCreated,
				BookID: string(rune('a' + i)),
			})
		}(i)
	}
	wg.Wait()

	assert.Len(t, fp.Events(), n)
}
