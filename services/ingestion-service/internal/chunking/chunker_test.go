package chunking

import (
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
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

type textPreservingTokenizer struct{}

func (textPreservingTokenizer) Encode(value string) []int {
	result := make([]int, 0, len(value))
	for _, char := range value {
		result = append(result, int(char))
	}
	return result
}

func (textPreservingTokenizer) Decode(tokens []int) string {
	result := make([]rune, len(tokens))
	for index, token := range tokens {
		result[index] = rune(token)
	}
	return string(result)
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

func TestChunkerEmitsExactFinalWindowOnce(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 4, OverlapTokens: 1, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := chunker.AddPage("book-1", Page{Number: 1, Text: "one two three four"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("AddPage emitted %d chunks, want none before final exact window", len(chunks))
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 1 {
		t.Fatalf("Finish emitted %d chunks, want one exact final window", len(final))
	}
	if final[0].TokenStart() != 0 || final[0].TokenEnd() != 4 {
		t.Fatalf("unexpected exact token bounds %d-%d", final[0].TokenStart(), final[0].TokenEnd())
	}
}

func TestChunkerDoesNotEmitOverlapOnlyChunkBeforeHeading(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 4, OverlapTokens: 1, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	first, err := chunker.AddPage("book-1", Page{Number: 1, Text: "one two three four"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("first page emitted %d chunks, want none before boundary", len(first))
	}
	flushed, err := chunker.AddPage("book-1", Page{Number: 2, Text: "Chapter I Fresh Start"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks := append(flushed, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected exact pre-heading chunk and heading chunk, got %d", len(chunks))
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 4 {
		t.Fatalf("unexpected pre-heading token bounds %d-%d", chunks[0].TokenStart(), chunks[0].TokenEnd())
	}
	if chunks[1].TokenStart() != 4 || chunks[1].TokenEnd() != 8 {
		t.Fatalf("unexpected heading token bounds %d-%d", chunks[1].TokenStart(), chunks[1].TokenEnd())
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
	if len(chunks) != 1 {
		t.Fatalf("expected one exact-window chunk, got %d", len(chunks))
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

func TestChunkerLimitsChunksToConfiguredPageWindow(t *testing.T) {
	chunker, err := New(textPreservingTokenizer{}, Policy{MaximumTokens: 100, OverlapTokens: 2, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, 0, 2)
	for _, page := range []Page{
		{Number: 1, Text: "one"},
		{Number: 2, Text: "two"},
		{Number: 3, Text: "three"},
		{Number: 4, Text: "four"},
	} {
		var emitted []domain.Chunk
		emitted, err = chunker.AddPage("book-1", page)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, emitted...)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected page-window chunk plus tail, got %d", len(chunks))
	}
	if chunks[0].PageStart() != 1 || chunks[0].PageEnd() != 3 {
		t.Fatalf("first chunk pages = %d-%d", chunks[0].PageStart(), chunks[0].PageEnd())
	}
	if chunks[1].PageStart() < 3 || chunks[1].PageEnd() != 4 {
		t.Fatalf("tail chunk pages = %d-%d", chunks[1].PageStart(), chunks[1].PageEnd())
	}
}

func TestChunkerCapsPageWindowChunksByMaximumTokens(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 4, OverlapTokens: 1, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, 0, 3)
	for _, page := range []Page{
		{Number: 1, Text: "one two"},
		{Number: 2, Text: "three four"},
		{Number: 3, Text: "five six"},
		{Number: 4, Text: "seven"},
	} {
		var emitted []domain.Chunk
		emitted, err = chunker.AddPage("book-1", page)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, emitted...)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	for _, chunk := range chunks {
		if chunk.TokenEnd()-chunk.TokenStart() > 4 {
			t.Fatalf("chunk exceeded token cap: %d-%d", chunk.TokenStart(), chunk.TokenEnd())
		}
	}
	for index := 1; index < len(chunks); index++ {
		previous := chunks[index-1]
		chunk := chunks[index]
		if chunk.TokenEnd() <= previous.TokenEnd() || chunk.TokenStart() > previous.TokenEnd() ||
			previous.TokenEnd()-chunk.TokenStart() > 1 {
			t.Fatalf("invalid adjacent bounds: previous=%d-%d current=%d-%d", previous.TokenStart(), previous.TokenEnd(), chunk.TokenStart(), chunk.TokenEnd())
		}
	}
}

func TestChunkerDoesNotRetainOversizedOverlapForShortSparsePageWindow(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 800, OverlapTokens: 120, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, 0, 2)
	for _, page := range []Page{
		{Number: 2, Text: "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen"},
		{Number: 4, Text: "   "},
		{Number: 5, Text: "eighteen"},
	} {
		var emitted []domain.Chunk
		emitted, err = chunker.AddPage("book-1", page)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, emitted...)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected sparse page-window chunk plus tail, got %d", len(chunks))
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 17 {
		t.Fatalf("first chunk token bounds = %d-%d", chunks[0].TokenStart(), chunks[0].TokenEnd())
	}
	if chunks[1].TokenStart() != 17 || chunks[1].TokenEnd() != 18 {
		t.Fatalf("tail chunk token bounds = %d-%d", chunks[1].TokenStart(), chunks[1].TokenEnd())
	}
}

func TestChunkerDropsOverlapOnlyRemainderAfterPageWindowEmit(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 800, OverlapTokens: 120, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, 0, 3)
	for _, page := range []Page{
		{Number: 1, Text: strings.Repeat("word ", 17)},
		{Number: 2, Text: strings.Repeat("word ", 65)},
		{Number: 3, Text: strings.Repeat("word ", 60)},
		{Number: 4, Text: strings.Repeat("word ", 60)},
		{Number: 5, Text: "tail"},
	} {
		var emitted []domain.Chunk
		emitted, err = chunker.AddPage("book-1", page)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, emitted...)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 3 {
		t.Fatalf("expected advancing page-window chunks and final tail, got %d", len(chunks))
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 142 {
		t.Fatalf("chunk token bounds = %d-%d", chunks[0].TokenStart(), chunks[0].TokenEnd())
	}
	if chunks[1].TokenStart() != 22 || chunks[1].TokenEnd() != 202 {
		t.Fatalf("second chunk token bounds = %d-%d", chunks[1].TokenStart(), chunks[1].TokenEnd())
	}
	if chunks[2].TokenStart() != 82 || chunks[2].TokenEnd() != 203 {
		t.Fatalf("tail chunk token bounds = %d-%d", chunks[2].TokenStart(), chunks[2].TokenEnd())
	}
	for index := 1; index < len(chunks); index++ {
		if chunks[index].TokenEnd() <= chunks[index-1].TokenEnd() {
			t.Fatalf("chunk %d did not advance token end: previous=%d-%d current=%d-%d", index, chunks[index-1].TokenStart(), chunks[index-1].TokenEnd(), chunks[index].TokenStart(), chunks[index].TokenEnd())
		}
	}
}

func TestChunkerDropsOverlapOnlyTailAtFinish(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 4, OverlapTokens: 2, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	emitted, err := chunker.AddPage("book-1", Page{Number: 1, Text: "one two three four five six"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks := append(emitted, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected first window plus final new tail, got %d", len(chunks))
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 4 {
		t.Fatalf("first chunk token bounds = %d-%d", chunks[0].TokenStart(), chunks[0].TokenEnd())
	}
	if chunks[1].TokenStart() != 2 || chunks[1].TokenEnd() != 6 {
		t.Fatalf("tail chunk token bounds = %d-%d", chunks[1].TokenStart(), chunks[1].TokenEnd())
	}

	chunker, err = New(wordTokenizer{}, Policy{MaximumTokens: 4, OverlapTokens: 2, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	emitted, err = chunker.AddPage("book-1", Page{Number: 1, Text: "one two three four five six"})
	if err != nil {
		t.Fatal(err)
	}
	more, err := chunker.AddPage("book-1", Page{Number: 1, Text: "seven eight"})
	if err != nil {
		t.Fatal(err)
	}
	emitted = append(emitted, more...)
	final, err = chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(emitted, final...)
	if len(chunks) != 3 {
		t.Fatalf("expected two windows plus final new tail, got %d", len(chunks))
	}
	for index := 1; index < len(chunks); index++ {
		previous := chunks[index-1]
		chunk := chunks[index]
		if chunk.TokenEnd() <= previous.TokenEnd() {
			t.Fatalf("chunk %d did not advance token end: previous=%d-%d current=%d-%d", index, previous.TokenStart(), previous.TokenEnd(), chunk.TokenStart(), chunk.TokenEnd())
		}
	}
}

func TestChunkerDropsOverlapOnlyTailBeforeNewChapter(t *testing.T) {
	chunker, err := New(textPreservingTokenizer{}, Policy{MaximumTokens: 100, OverlapTokens: 2, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, 0, 2)
	for _, page := range []Page{
		{Number: 1, Text: "one"},
		{Number: 2, Text: "two"},
		{Number: 3, Text: "three"},
		{Number: 4, Text: "Chapter I Start"},
	} {
		var emitted []domain.Chunk
		emitted, err = chunker.AddPage("book-1", page)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, emitted...)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected previous window and new chapter only, got %d", len(chunks))
	}
	if chunks[0].PageEnd() != 3 {
		t.Fatalf("previous chapter chunk ended on page %d", chunks[0].PageEnd())
	}
	if chunks[1].PageStart() != 4 || chunks[1].Chapter() != "Chapter I Start" {
		t.Fatalf("new chapter chunk = text=%q pages=%d-%d chapter=%q", chunks[1].Text(), chunks[1].PageStart(), chunks[1].PageEnd(), chunks[1].Chapter())
	}
}

func TestChunkerDropsLargeBoundaryOverlapOnlyTailBeforeNewChapter(t *testing.T) {
	chunker, err := New(wordTokenizer{}, Policy{MaximumTokens: 185, OverlapTokens: 120, TargetPages: 2, MaximumPages: 3, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, 0, 2)
	emitted, err := chunker.AddPage("book-1", Page{Number: 1, Text: strings.Repeat("word ", 202)})
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, emitted...)
	emitted, err = chunker.AddPage("book-1", Page{Number: 2, Text: "Chapter I Start"})
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, emitted...)
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 3 {
		t.Fatalf("expected two windows plus new chapter, got %d", len(chunks))
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 185 {
		t.Fatalf("first chunk token bounds = %d-%d", chunks[0].TokenStart(), chunks[0].TokenEnd())
	}
	if chunks[1].TokenStart() != 65 || chunks[1].TokenEnd() != 202 {
		t.Fatalf("second chunk token bounds = %d-%d", chunks[1].TokenStart(), chunks[1].TokenEnd())
	}
	if chunks[2].TokenStart() != 202 || chunks[2].Chapter() != "Chapter I Start" {
		t.Fatalf("new chapter chunk = token_start=%d chapter=%q", chunks[2].TokenStart(), chunks[2].Chapter())
	}
}

func TestChunkerPreservesCrossPageSeparation(t *testing.T) {
	chunkDocument := func(t *testing.T) []domain.Chunk {
		t.Helper()
		chunker, err := New(textPreservingTokenizer{}, Policy{MaximumTokens: 20, OverlapTokens: 2, MaximumChunks: 10})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = chunker.AddPage("book-1", Page{Number: 1, Text: "hello"}); err != nil {
			t.Fatal(err)
		}
		if _, err = chunker.AddPage("book-1", Page{Number: 2, Text: "world"}); err != nil {
			t.Fatal(err)
		}
		chunks, err := chunker.Finish("book-1")
		if err != nil {
			t.Fatal(err)
		}
		return chunks
	}

	first := chunkDocument(t)
	second := chunkDocument(t)
	if len(first) != 1 {
		t.Fatalf("expected one chunk, got %d", len(first))
	}
	chunk := first[0]
	if chunk.Text() != "hello\nworld" {
		t.Fatalf("expected page separator, got %q", chunk.Text())
	}
	if chunk.PageStart() != 1 || chunk.PageEnd() != 2 {
		t.Fatalf("expected pages 1-2, got %d-%d", chunk.PageStart(), chunk.PageEnd())
	}
	if chunk.TokenStart() != 0 || chunk.TokenEnd() != 11 {
		t.Fatalf("expected token bounds 0-11, got %d-%d", chunk.TokenStart(), chunk.TokenEnd())
	}
	if len(second) != 1 || second[0].ID() != chunk.ID() {
		t.Fatal("expected deterministic chunk ID")
	}
}

func TestChunkerAttributesCrossPageSeparatorToIncomingPageDuringOverlap(t *testing.T) {
	chunker, err := New(textPreservingTokenizer{}, Policy{MaximumTokens: 7, OverlapTokens: 2, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = chunker.AddPage("book-1", Page{Number: 1, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	chunks, err := chunker.AddPage("book-1", Page{Number: 2, Text: "world"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %d", len(chunks))
	}
	if chunks[0].Text() != "hello\nw" || chunks[0].PageStart() != 1 || chunks[0].PageEnd() != 2 {
		t.Fatalf("unexpected first chunk: text=%q pages=%d-%d", chunks[0].Text(), chunks[0].PageStart(), chunks[0].PageEnd())
	}
	if chunks[1].Text() != "world" || chunks[1].PageStart() != 2 || chunks[1].PageEnd() != 2 {
		t.Fatalf("unexpected overlap chunk: text=%q pages=%d-%d", chunks[1].Text(), chunks[1].PageStart(), chunks[1].PageEnd())
	}
	if chunks[0].TokenStart() != 0 || chunks[0].TokenEnd() != 7 || chunks[1].TokenStart() != 5 || chunks[1].TokenEnd() != 11 {
		t.Fatalf("unexpected overlap token bounds: %d-%d, %d-%d", chunks[0].TokenStart(), chunks[0].TokenEnd(), chunks[1].TokenStart(), chunks[1].TokenEnd())
	}
}

func TestChunkerInsertsOneSeparatorAcrossBlankPages(t *testing.T) {
	chunker, err := New(textPreservingTokenizer{}, Policy{MaximumTokens: 20, OverlapTokens: 2, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []Page{
		{Number: 1, Text: "hello"},
		{Number: 2, Text: " \n\t "},
		{Number: 3, Text: "world"},
	} {
		if _, err = chunker.AddPage("book-1", page); err != nil {
			t.Fatal(err)
		}
	}
	chunks, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Text() != "hello\nworld" {
		t.Fatalf("expected one separator across blank page, got %#v", chunks)
	}
	if chunks[0].PageStart() != 1 || chunks[0].PageEnd() != 3 {
		t.Fatalf("expected pages 1-3, got %d-%d", chunks[0].PageStart(), chunks[0].PageEnd())
	}
}

func TestChunkerDoesNotCarrySeparatorAcrossHeadingFlush(t *testing.T) {
	chunker, err := New(textPreservingTokenizer{}, Policy{MaximumTokens: 40, OverlapTokens: 2, MaximumChunks: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = chunker.AddPage("book-1", Page{Number: 1, Text: "preface"}); err != nil {
		t.Fatal(err)
	}
	flushed, err := chunker.AddPage("book-1", Page{Number: 2, Text: "Chapter I Start\nbody"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := chunker.Finish("book-1")
	if err != nil {
		t.Fatal(err)
	}
	chunks := append(flushed, final...)
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %d", len(chunks))
	}
	if chunks[0].Text() != "preface" || chunks[0].PageStart() != 1 || chunks[0].PageEnd() != 1 {
		t.Fatalf("unexpected flushed chunk: text=%q pages=%d-%d", chunks[0].Text(), chunks[0].PageStart(), chunks[0].PageEnd())
	}
	if chunks[1].Text() != "Chapter I Start\nbody" || chunks[1].PageStart() != 2 || chunks[1].PageEnd() != 2 {
		t.Fatalf("unexpected heading chunk: text=%q pages=%d-%d", chunks[1].Text(), chunks[1].PageStart(), chunks[1].PageEnd())
	}
}

func TestNormalizeRemovesUnsafeControlsAndPreservesLines(t *testing.T) {
	got := Normalize("A\r\nB\x00\tC\n")
	if got != "A\nB\tC" {
		t.Fatalf("unexpected normalized text %q", got)
	}
}
