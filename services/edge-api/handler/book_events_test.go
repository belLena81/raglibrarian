package handler

import (
	"testing"
	"time"
)

func TestBookStatusHubCoalescesPerBookWithoutDroppingOtherBooks(t *testing.T) {
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)
	subscriber, remove, ok := hub.subscribe("session", "192.0.2.1")
	if !ok {
		t.Fatal("subscription was rejected")
	}
	defer remove()

	hub.Publish(BookStatusEvent{EventID: "book-a-v1", BookID: "book-a", ProcessingVersion: 1})
	hub.Publish(BookStatusEvent{EventID: "book-b-v1", BookID: "book-b", ProcessingVersion: 1})
	hub.Publish(BookStatusEvent{EventID: "book-a-v2", BookID: "book-a", ProcessingVersion: 2})

	waitForBookStatusWake(t, subscriber)
	events, resync, subscribed := hub.drain(subscriber)
	if !subscribed || resync {
		t.Fatalf("drain = events=%v resync=%t subscribed=%t, want status events", events, resync, subscribed)
	}
	if len(events) != 2 {
		t.Fatalf("received %d events, want one for each book", len(events))
	}
	byBook := make(map[string]BookStatusEvent, len(events))
	for _, event := range events {
		byBook[event.BookID] = event
	}
	if event := byBook["book-a"]; event.EventID != "book-a-v2" || event.ProcessingVersion != 2 {
		t.Fatalf("book-a event = %+v, want latest version", event)
	}
	if event := byBook["book-b"]; event.EventID != "book-b-v1" || event.ProcessingVersion != 1 {
		t.Fatalf("book-b event = %+v, want independent status", event)
	}
}

func TestBookStatusHubOverflowRequestsOneResyncUntilDrained(t *testing.T) {
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)
	subscriber, remove, ok := hub.subscribe("session", "192.0.2.1")
	if !ok {
		t.Fatal("subscription was rejected")
	}
	defer remove()

	for book := 0; book <= maxPendingBookStatusEvents; book++ {
		hub.Publish(BookStatusEvent{BookID: string(rune(book + 1)), ProcessingVersion: 1})
	}
	// Further events must not replace the durable resync signal.
	hub.Publish(BookStatusEvent{BookID: "later-book", ProcessingVersion: 1})

	waitForBookStatusWake(t, subscriber)
	events, resync, subscribed := hub.drain(subscriber)
	if !subscribed || !resync || len(events) != 0 {
		t.Fatalf("drain = events=%v resync=%t subscribed=%t, want one resync", events, resync, subscribed)
	}
	assertNoBookStatusWake(t, subscriber)

	hub.Publish(BookStatusEvent{EventID: "resumed", BookID: "resumed-book", ProcessingVersion: 1})
	waitForBookStatusWake(t, subscriber)
	events, resync, subscribed = hub.drain(subscriber)
	if !subscribed || resync || len(events) != 1 || events[0].EventID != "resumed" {
		t.Fatalf("drain after resync = events=%v resync=%t subscribed=%t, want resumed event", events, resync, subscribed)
	}
}

func TestBookStatusHubRemainsLiveAcrossPublishAndSubscriptionLifecycle(t *testing.T) {
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)

	first, unsubscribeFirst, ok := hub.subscribe("session", "192.0.2.1")
	if !ok {
		t.Fatal("first subscription was rejected")
	}
	hub.Publish(BookStatusEvent{EventID: "first", BookID: "book", ProcessingVersion: 1})
	event := receiveBookStatusEvent(t, hub, first)
	if event.EventID != "first" || event.SchemaVersion != 1 {
		t.Fatalf("received first event %+v, want ID first and schema version 1", event)
	}
	unsubscribeFirst()
	assertBookStatusChannelClosed(t, first.wake)

	second, unsubscribeSecond, ok := hub.subscribe("session", "192.0.2.1")
	if !ok {
		t.Fatal("subscription after unsubscribe was rejected")
	}
	hub.Publish(BookStatusEvent{EventID: "second", BookID: "book", ProcessingVersion: 1})
	if event := receiveBookStatusEvent(t, hub, second); event.EventID != "second" {
		t.Fatalf("received event %q, want second", event.EventID)
	}

	hub.SetAvailable(false)
	assertBookStatusChannelClosed(t, second.wake)
	unsubscribeSecond()
	if _, _, available := hub.subscribe("other-session", "192.0.2.2"); available {
		t.Fatal("subscription succeeded while hub was unavailable")
	}

	hub.SetAvailable(true)
	third, unsubscribeThird, ok := hub.subscribe("other-session", "192.0.2.2")
	if !ok {
		t.Fatal("subscription after availability recovery was rejected")
	}
	hub.Publish(BookStatusEvent{EventID: "third", BookID: "book", ProcessingVersion: 1})
	if event := receiveBookStatusEvent(t, hub, third); event.EventID != "third" {
		t.Fatalf("received event %q, want third", event.EventID)
	}
	unsubscribeThird()
}

func receiveBookStatusEvent(t *testing.T, hub *BookStatusHub, subscriber *bookStatusSubscriber) BookStatusEvent {
	t.Helper()
	waitForBookStatusWake(t, subscriber)
	events, resync, subscribed := hub.drain(subscriber)
	if !subscribed || resync || len(events) != 1 {
		t.Fatalf("drain = events=%v resync=%t subscribed=%t, want one status event", events, resync, subscribed)
	}
	return events[0]
}

func waitForBookStatusWake(t *testing.T, subscriber *bookStatusSubscriber) {
	t.Helper()
	select {
	case _, open := <-subscriber.wake:
		if !open {
			t.Fatal("subscriber wake channel closed before notification")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber notification")
	}
}

func assertNoBookStatusWake(t *testing.T, subscriber *bookStatusSubscriber) {
	t.Helper()
	select {
	case <-subscriber.wake:
		t.Fatal("received duplicate subscriber notification")
	default:
	}
}

func assertBookStatusChannelClosed(t *testing.T, subscriber <-chan struct{}) {
	t.Helper()
	select {
	case _, open := <-subscriber:
		if open {
			t.Fatal("subscriber channel remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber channel to close")
	}
}
