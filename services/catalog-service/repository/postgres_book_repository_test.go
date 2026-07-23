package repository

import (
	"strings"
	"testing"
)

func TestFinalDeletionStatusEventIDIsEdgeCompatible(t *testing.T) {
	eventID := finalDeletionStatusEventID(
		"book-1",
		"delete-command-with:colon:"+strings.Repeat("x", 256),
		7,
		12,
	)
	if strings.Contains(eventID, ":") {
		t.Fatalf("final deletion event ID contains a colon: %q", eventID)
	}
	if len(eventID) > 128 {
		t.Fatalf("final deletion event ID length = %d, want <= 128", len(eventID))
	}
	for _, char := range eventID {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if char >= 'A' && char <= 'Z' {
			continue
		}
		if char >= '0' && char <= '9' {
			continue
		}
		if char == '-' || char == '_' {
			continue
		}
		t.Fatalf("final deletion event ID contains unsupported character %q in %q", char, eventID)
	}
}

func TestFinalDeletionStatusEventIDIsDeterministicAndVersioned(t *testing.T) {
	eventID := finalDeletionStatusEventID("book-1", "delete-command-1", 7, 12)
	if repeat := finalDeletionStatusEventID("book-1", "delete-command-1", 7, 12); repeat != eventID {
		t.Fatalf("final deletion event ID changed across identical inputs: %q != %q", repeat, eventID)
	}
	if nextVersion := finalDeletionStatusEventID("book-1", "delete-command-1", 7, 13); nextVersion == eventID {
		t.Fatalf("final deletion event ID did not change across processing versions: %q", eventID)
	}
}
