package chunking

import (
	"strings"
	"testing"
)

type wordTokenizer struct{}

func (wordTokenizer) Encode(value string) []int {
	words := strings.Fields(value)
	result := make([]int, len(words))
	for index := range words {
		result[index] = index
	}
	return result
}

func (wordTokenizer) Decode(tokens []int) string {
	parts := make([]string, len(tokens))
	for index, token := range tokens {
		parts[index] = string(rune('a' + token))
	}
	return strings.Join(parts, " ")
}

func TestChunkerUsesBoundedOverlappingWindows(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 4, OverlapTokens: 1, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := chunker.AddPage("book-1", Page{Number: 1, Text: "one two three four five six"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks after finish, got %d", len(chunks))
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 4 || chunks[1].TokenStart() != 3 || chunks[1].TokenEnd() != 6 {
		t.Fatalf("unexpected token bounds")
	}
}

func TestChunkerCarriesStructureAcrossPagesAndSpansPages(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 7, OverlapTokens: 1, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	first, err := chunker.AddPage("book-1", Page{Number: 1, Text: "Chapter IV Safe Example\nalpha"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := chunker.AddPage("book-1", Page{Number: 2, Text: "beta gamma"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks := append(append(first, second...), final...)
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %d", len(chunks))
	}
	if chunks[0].PageStart() != 1 || chunks[0].PageEnd() != 2 {
		t.Fatalf("expected cross-page span, got %d-%d", chunks[0].PageStart(), chunks[0].PageEnd())
	}
	for _, chunk := range chunks {
		if chunk.Chapter() != "Chapter IV Safe Example" {
			t.Fatalf("chapter context was not carried: %q", chunk.Chapter())
		}
	}
}

func TestNormalizeRemovesUnsafeControlsAndPreservesLines(t *testing.T) {
	got := Normalize("A\r\nB\x00\tC\n")
	if got != "A\nB\tC" {
		t.Fatalf("unexpected normalized text %q", got)
	}
}
