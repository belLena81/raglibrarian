package main

import (
	"bytes"
	"testing"
)

func TestGenerateCodeReturnsBase32Code(t *testing.T) {
	originalReader := randomReader
	t.Cleanup(func() { randomReader = originalReader })
	randomReader = bytes.NewReader(make([]byte, 20))

	code, err := generateCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 32 {
		t.Fatalf("code length = %d, want 32", len(code))
	}
	for _, character := range code {
		if !(character >= 'A' && character <= 'Z') && !(character >= '2' && character <= '7') {
			t.Fatalf("code contains non-base32 character %q", character)
		}
	}
}
