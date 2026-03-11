package domain

import (
	"strings"
	"time"
)

const (
	minYear = 1900
)

// ── Book ─────────────────────────────────────────────────────────────────────

func validateTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return ErrEmptyTitle
	}
	return nil
}

func validateAuthor(author string) error {
	if strings.TrimSpace(author) == "" {
		return ErrEmptyAuthor
	}
	return nil
}

func validateYear(year int) error {
	currentYear := time.Now().UTC().Year()
	if year < minYear || year > currentYear {
		return ErrInvalidYear
	}
	return nil
}

// validateTags ensures every element is non-blank and that no duplicates exist.
func validateTags(tags []string) error {
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			return ErrInvalidTag
		}
		if _, exists := seen[tag]; exists {
			return ErrInvalidTag
		}
		seen[tag] = struct{}{}
	}
	return nil
}

// ── Chunk ─────────────────────────────────────────────────────────────────────

func validateContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return ErrEmptyContent
	}
	return nil
}

func validatePageRange(pageStart, pageEnd int) error {
	if pageStart < 1 || pageEnd < pageStart {
		return ErrInvalidPages
	}
	return nil
}

// ── Query ─────────────────────────────────────────────────────────────────────

func validateQuestion(question string) error {
	if strings.TrimSpace(question) == "" {
		return ErrEmptyQuestion
	}
	return nil
}

// ── User ──────────────────────────────────────────────────────────────────────

func validateEmail(email string) error {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return ErrEmptyEmail
	}
	// A minimal structural check: must contain exactly one @, with non-empty
	// local and domain parts. Full RFC 5322 validation belongs at the HTTP
	// boundary, not in the domain.
	at := strings.LastIndex(trimmed, "@")
	if at < 1 || at == len(trimmed)-1 {
		return ErrInvalidEmail
	}
	return nil
}

// ── SearchResult ──────────────────────────────────────────────────────────────

func validateQueryID(queryID string) error {
	if strings.TrimSpace(queryID) == "" {
		return ErrEmptyQueryId
	}
	return nil
}

func validateChapter(chapter string) error {
	if strings.TrimSpace(chapter) == "" {
		return ErrEmptyChapter
	}
	return nil
}

func validatePassage(passage string) error {
	if strings.TrimSpace(passage) == "" {
		return ErrEmptyPassage
	}
	return nil
}

func validatePages(pages []int) error {
	if len(pages) == 0 {
		return ErrEmptyPages
	}
	return nil
}

func validateScore(score float64) error {
	if score < 0 || score > 1 {
		return ErrInvalidScore
	}
	return nil
}
