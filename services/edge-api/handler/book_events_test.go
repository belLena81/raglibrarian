package handler

import (
	"testing"
	"time"
)

func TestReplaceLatestBookStatusEventReplacesFullBuffer(t *testing.T) {
	subscriber := make(chan BookStatusEvent, 1)
	subscriber <- BookStatusEvent{EventID: "stale"}

	replaceLatestBookStatusEvent(subscriber, BookStatusEvent{EventID: "latest"})

	event := receiveBookStatusEvent(t, subscriber)
	if event.EventID != "latest" {
		t.Fatalf("received event %q, want latest", event.EventID)
	}
}

func TestReplaceLatestBookStatusEventReturnsWhenReceiverDrainedBuffer(t *testing.T) {
	subscriber := make(chan BookStatusEvent, 1)
	subscriber <- BookStatusEvent{EventID: "stale"}
	<-subscriber
	done := make(chan struct{})

	go func() {
		replaceLatestBookStatusEvent(subscriber, BookStatusEvent{EventID: "latest"})
		close(done)
	}()

	waitForBookStatusOperation(t, done)
	event := receiveBookStatusEvent(t, subscriber)
	if event.EventID != "latest" {
		t.Fatalf("received event %q, want latest", event.EventID)
	}
}

func TestReplaceLatestBookStatusEventDropsForUnbufferedSubscriber(t *testing.T) {
	subscriber := make(chan BookStatusEvent)
	done := make(chan struct{})

	go func() {
		replaceLatestBookStatusEvent(subscriber, BookStatusEvent{EventID: "latest"})
		close(done)
	}()

	waitForBookStatusOperation(t, done)
}

func TestBookStatusHubRemainsLiveAcrossPublishAndSubscriptionLifecycle(t *testing.T) {
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)

	first, unsubscribeFirst, ok := hub.subscribe("session", "192.0.2.1")
	if !ok {
		t.Fatal("first subscription was rejected")
	}
	hub.Publish(BookStatusEvent{EventID: "first"})
	firstEvent := receiveBookStatusEvent(t, first)
	if firstEvent.EventID != "first" || firstEvent.SchemaVersion != 1 {
		t.Fatalf("received first event %+v, want ID first and schema version 1", firstEvent)
	}
	unsubscribeFirst()
	assertBookStatusChannelClosed(t, first)

	second, unsubscribeSecond, ok := hub.subscribe("session", "192.0.2.1")
	if !ok {
		t.Fatal("subscription after unsubscribe was rejected")
	}
	hub.Publish(BookStatusEvent{EventID: "second"})
	secondEvent := receiveBookStatusEvent(t, second)
	if secondEvent.EventID != "second" {
		t.Fatalf("received event %q, want second", secondEvent.EventID)
	}

	hub.SetAvailable(false)
	assertBookStatusChannelClosed(t, second)
	unsubscribeSecond()
	if _, _, available := hub.subscribe("other-session", "192.0.2.2"); available {
		t.Fatal("subscription succeeded while hub was unavailable")
	}

	hub.SetAvailable(true)
	third, unsubscribeThird, ok := hub.subscribe("other-session", "192.0.2.2")
	if !ok {
		t.Fatal("subscription after availability recovery was rejected")
	}
	hub.Publish(BookStatusEvent{EventID: "third"})
	if event := receiveBookStatusEvent(t, third); event.EventID != "third" {
		t.Fatalf("received event %q, want third", event.EventID)
	}
	unsubscribeThird()
}

func receiveBookStatusEvent(t *testing.T, subscriber <-chan BookStatusEvent) BookStatusEvent {
	t.Helper()
	select {
	case event, open := <-subscriber:
		if !open {
			t.Fatal("subscriber channel closed before event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber event")
		return BookStatusEvent{}
	}
}

func assertBookStatusChannelClosed(t *testing.T, subscriber <-chan BookStatusEvent) {
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

func waitForBookStatusOperation(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("book status operation did not return")
	}
}
