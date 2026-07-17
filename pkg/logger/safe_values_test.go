package logger

import "testing"

func TestMaskedEmail(t *testing.T) {
	tests := map[string]string{
		"bob@example.com":       "b***@e***.com",
		"a@b.co":                "a***@b***.co",
		"bob@mail.example.com":  "b***@m***.e***.com",
		"δοκιμή@παράδειγμα.ελ":  "δ***@π***.ελ",
		"malformed":             "invalid-email",
		"bob@example":           "invalid-email",
		"bob@exam\nple.com":     "invalid-email",
		"bob@exam\x1bple.com":   "invalid-email",
		"bob@exam\u2028ple.com": "invalid-email",
		"":                      "invalid-email",
	}
	for input, want := range tests {
		if got := MaskedEmail(input).String(); got != want {
			t.Errorf("MaskedEmail(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBookSummaryReplacesAllControls(t *testing.T) {
	summary := BookSummary{Title: "one\x1btwo\x00three\vfour\u2028five\u2029six"}.String()
	if summary != `book id="" title="one?two?three?four?five?six" author="" year=0 status=""` {
		t.Fatalf("summary = %q", summary)
	}
}

func TestBookSummaryEscapesAndBoundsUntrustedText(t *testing.T) {
	summary := BookSummary{ID: "id", Title: "line one\nline two", Author: "author", Year: 2026, Status: "pending"}.String()
	if summary != `book id="id" title="line one?line two" author="author" year=2026 status="pending"` {
		t.Fatalf("summary = %q", summary)
	}
}
