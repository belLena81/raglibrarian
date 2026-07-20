// Package chunking provides deterministic normalization and stateful token-aware chunking.
package chunking

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"golang.org/x/text/unicode/norm"
)

const (
	NormalizationVersion = "nfc-v1"
	ChunkingVersion      = "token-window-v2"
	StructureVersion     = "heading-carry-v1"
	DefaultMaximumTokens = 800
	DefaultOverlapTokens = 120
)

var ErrChunkLimit = errors.New("chunk limit exceeded")

type Tokenizer interface {
	Encode(string) []int
	Decode([]int) string
}

type Policy struct {
	MaximumTokens int
	OverlapTokens int
	MaximumChunks int
}

type Page struct {
	Number uint32
	Text   string
}

// StructureContext carries the most recently observed document headings. It is
// deliberately independent of parser and protobuf types.
type StructureContext struct {
	Chapter string
	Section string
}

type bufferedToken struct {
	value  int
	page   uint32
	global uint64
}

// Chunker is a per-document state machine. It retains a partial window between
// pages so chunks can cross page boundaries without losing structural context.
type Chunker struct {
	tokenizer  Tokenizer
	policy     Policy
	nextOrder  uint64
	nextToken  uint64
	chunkCount int
	buffer     []bufferedToken
	structure  StructureContext
	finished   bool
}

func New(tokenizer Tokenizer, policy Policy) (*Chunker, error) {
	if tokenizer == nil || policy.MaximumTokens < 1 || policy.OverlapTokens < 0 || policy.OverlapTokens >= policy.MaximumTokens || policy.MaximumChunks < 1 {
		return nil, errors.New("invalid chunking policy")
	}
	return &Chunker{tokenizer: tokenizer, policy: policy}, nil
}

func (c *Chunker) AddPage(bookID string, page Page) ([]domain.Chunk, error) {
	if c.finished || page.Number == 0 || strings.TrimSpace(bookID) == "" {
		return nil, errors.New("invalid page")
	}
	text := Normalize(page.Text)
	if text == "" {
		return nil, nil
	}

	heading := detectHeading(text)
	result := make([]domain.Chunk, 0, 2)
	if heading.Chapter != "" || heading.Section != "" {
		flushed, err := c.flushBoundary(bookID)
		if err != nil {
			return nil, err
		}
		result = append(result, flushed...)
		if heading.Chapter != "" {
			c.structure.Chapter = heading.Chapter
			c.structure.Section = ""
		}
		if heading.Section != "" {
			c.structure.Section = heading.Section
		}
	}

	if len(c.buffer) > 0 {
		for _, token := range c.tokenizer.Encode("\n") {
			c.buffer = append(c.buffer, bufferedToken{value: token, page: page.Number, global: c.nextToken})
			c.nextToken++
		}
	}
	tokens := c.tokenizer.Encode(text)
	for _, token := range tokens {
		c.buffer = append(c.buffer, bufferedToken{value: token, page: page.Number, global: c.nextToken})
		c.nextToken++
	}
	for len(c.buffer) > c.policy.MaximumTokens {
		chunk, err := c.emit(bookID, c.policy.MaximumTokens)
		if err != nil {
			return nil, err
		}
		result = append(result, chunk)
		c.advanceWindow(c.policy.MaximumTokens - c.policy.OverlapTokens)
	}
	return result, nil
}

// Finish emits the final partial window. Calling it more than once is invalid,
// which catches accidental reuse of a document-scoped chunker.
func (c *Chunker) Finish(bookID string) ([]domain.Chunk, error) {
	if c.finished || strings.TrimSpace(bookID) == "" {
		return nil, errors.New("chunker already finished")
	}
	c.finished = true
	return c.flushBoundary(bookID)
}

func (c *Chunker) flushBoundary(bookID string) ([]domain.Chunk, error) {
	result := make([]domain.Chunk, 0, 1)
	for len(c.buffer) > 0 {
		window := min(len(c.buffer), c.policy.MaximumTokens)
		chunk, err := c.emit(bookID, window)
		if err != nil {
			return nil, err
		}
		result = append(result, chunk)
		// A semantic boundary must not carry overlap into the next structure.
		if window == len(c.buffer) {
			c.buffer = c.buffer[:0]
		} else {
			c.advanceWindow(c.policy.MaximumTokens - c.policy.OverlapTokens)
		}
	}
	return result, nil
}

func (c *Chunker) emit(bookID string, size int) (domain.Chunk, error) {
	if c.chunkCount >= c.policy.MaximumChunks {
		return domain.Chunk{}, ErrChunkLimit
	}
	window := c.buffer[:size]
	tokens := make([]int, len(window))
	for index := range window {
		tokens[index] = window[index].value
	}
	text := strings.TrimSpace(c.tokenizer.Decode(tokens))
	if text == "" {
		return domain.Chunk{}, errors.New("tokenizer produced empty chunk")
	}
	pageStart := window[0].page
	pageEnd := window[len(window)-1].page
	tokenStart := window[0].global
	tokenEnd := window[len(window)-1].global + 1
	id := stableChunkID(bookID, c.nextOrder, pageStart, pageEnd, tokenStart, tokenEnd, c.structure, c.policy, text)
	chunk, err := domain.NewChunk(domain.ChunkInput{
		ID:         id,
		BookID:     bookID,
		Order:      c.nextOrder,
		Text:       text,
		Chapter:    c.structure.Chapter,
		Section:    c.structure.Section,
		PageStart:  pageStart,
		PageEnd:    pageEnd,
		TokenStart: tokenStart,
		TokenEnd:   tokenEnd,
	})
	if err != nil {
		return domain.Chunk{}, err
	}
	c.nextOrder++
	c.chunkCount++
	return chunk, nil
}

func (c *Chunker) advanceWindow(count int) {
	copy(c.buffer, c.buffer[count:])
	c.buffer = c.buffer[:len(c.buffer)-count]
}

func Normalize(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || !unicode.IsControl(char) {
			return char
		}
		return -1
	}, value)
	lines := strings.Split(norm.NFC.String(value), "\n")
	for index := range lines {
		lines[index] = strings.TrimRightFunc(lines[index], unicode.IsSpace)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var numberedHeading = regexp.MustCompile(`(?i)^(chapter|part|section)\s+(?:[0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten)(?:\s*[:.\-]\s*|\s+|$)(.{0,200})$`)

func detectHeading(text string) StructureContext {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		match := numberedHeading.FindStringSubmatch(line)
		if match == nil {
			return StructureContext{}
		}
		if strings.EqualFold(match[1], "chapter") || strings.EqualFold(match[1], "part") {
			return StructureContext{Chapter: line}
		}
		return StructureContext{Section: line}
	}
	return StructureContext{}
}

func stableChunkID(bookID string, order uint64, pageStart, pageEnd uint32, tokenStart, tokenEnd uint64, structure StructureContext, policy Policy, text string) string {
	contentSum := sha256.Sum256([]byte(text))
	identity := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s", StructureVersion, bookID, ChunkingVersion, policy.MaximumTokens, policy.OverlapTokens, order, pageStart, pageEnd, tokenStart, tokenEnd, structure.Chapter+"\x00"+structure.Section, hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}
