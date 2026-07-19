// Package chunking provides deterministic normalization and token-aware chunking.
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
	ChunkingVersion      = "token-window-v1"
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

type Chunker struct {
	tokenizer  Tokenizer
	policy     Policy
	nextOrder  uint64
	tokenBase  uint64
	chunkCount int
}

func New(tokenizer Tokenizer, policy Policy) (*Chunker, error) {
	if tokenizer == nil || policy.MaximumTokens < 1 || policy.OverlapTokens < 0 || policy.OverlapTokens >= policy.MaximumTokens || policy.MaximumChunks < 1 {
		return nil, errors.New("invalid chunking policy")
	}
	return &Chunker{tokenizer: tokenizer, policy: policy}, nil
}

func (c *Chunker) AddPage(bookID string, page Page) ([]domain.Chunk, error) {
	if page.Number == 0 {
		return nil, errors.New("invalid page number")
	}
	text := Normalize(page.Text)
	if text == "" {
		return nil, nil
	}
	tokens := c.tokenizer.Encode(text)
	if len(tokens) == 0 {
		return nil, nil
	}
	chapter, section := detectHeading(text)
	step := c.policy.MaximumTokens - c.policy.OverlapTokens
	result := make([]domain.Chunk, 0, (len(tokens)+step-1)/step)
	for start := 0; start < len(tokens); start += step {
		end := min(start+c.policy.MaximumTokens, len(tokens))
		chunkText := strings.TrimSpace(c.tokenizer.Decode(tokens[start:end]))
		if chunkText == "" {
			continue
		}
		if c.chunkCount >= c.policy.MaximumChunks {
			return nil, ErrChunkLimit
		}
		chunkID := stableChunkID(bookID, c.nextOrder, page.Number, chunkText)
		tokenStart := uint64(start) // #nosec G115 -- indexes are non-negative and bounded by accepted page bytes.
		tokenEnd := uint64(end)     // #nosec G115 -- indexes are non-negative and bounded by accepted page bytes.
		chunk, err := domain.NewChunk(domain.ChunkInput{
			ID:         chunkID,
			BookID:     bookID,
			Order:      c.nextOrder,
			Text:       chunkText,
			Chapter:    chapter,
			Section:    section,
			PageStart:  page.Number,
			PageEnd:    page.Number,
			TokenStart: c.tokenBase + tokenStart,
			TokenEnd:   c.tokenBase + tokenEnd,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, chunk)
		c.nextOrder++
		c.chunkCount++
		if end == len(tokens) {
			break
		}
	}
	c.tokenBase += uint64(len(tokens))
	return result, nil
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

var numberedHeading = regexp.MustCompile(`(?i)^(chapter|part|section)\s+[0-9ivxlcdm]+(?:\s*[:.-]\s*|\s+)(.{1,200})$`)

func detectHeading(text string) (string, string) {
	first, _, _ := strings.Cut(text, "\n")
	first = strings.TrimSpace(first)
	match := numberedHeading.FindStringSubmatch(first)
	if match == nil {
		return "", ""
	}
	if strings.EqualFold(match[1], "chapter") || strings.EqualFold(match[1], "part") {
		return first, ""
	}
	return "", first
}

func stableChunkID(bookID string, order uint64, page uint32, text string) string {
	contentSum := sha256.Sum256([]byte(text))
	identity := fmt.Sprintf("%s\x00%d\x00%d\x00%s", bookID, order, page, hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}
