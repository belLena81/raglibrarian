package main

import "testing"

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
