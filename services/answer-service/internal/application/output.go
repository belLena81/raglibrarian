package application

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

func validateSegments(values []domain.AnswerSegment, evidence []domain.ContextEvidence, limits Limits) ([]domain.AnswerSegment, error) {
	if len(values) == 0 || len(values) > limits.MaximumSegments {
		return nil, errors.New("invalid provider output")
	}
	allowed := make(map[string]struct{}, len(evidence))
	for _, value := range evidence {
		allowed[value.EvidenceID] = struct{}{}
	}
	total := 0
	result := make([]domain.AnswerSegment, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value.Text)
		if text == "" ||
			!utf8.ValidString(text) ||
			strings.ContainsRune(text, utf8.RuneError) ||
			strings.IndexFunc(text, unsafeAnswerRune) >= 0 ||
			len(value.EvidenceIDs) == 0 ||
			len(value.EvidenceIDs) > limits.MaximumCitations {
			return nil, errors.New("invalid provider output")
		}
		groundedText := groundedSegmentText(text, value.EvidenceIDs, evidence)
		total += len(groundedText)
		if total > limits.MaximumAnswerBytes {
			return nil, errors.New("invalid provider output")
		}
		seen := make(map[string]struct{}, len(value.EvidenceIDs))
		citations := make([]string, 0, len(value.EvidenceIDs))
		for _, id := range value.EvidenceIDs {
			if _, found := allowed[id]; !found {
				return nil, errors.New("invalid provider output")
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("invalid provider output")
			}
			seen[id] = struct{}{}
			citations = append(citations, id)
		}
		result = append(result, domain.AnswerSegment{Text: groundedText, EvidenceIDs: citations})
	}
	return result, nil
}

func groundedSegmentText(text string, evidenceIDs []string, evidence []domain.ContextEvidence) string {
	if len(evidenceIDs) == 0 {
		return text
	}
	var selected domain.ContextEvidence
	for _, value := range evidence {
		if value.EvidenceID == evidenceIDs[0] {
			selected = value
			break
		}
	}
	title := humanField(selected.Title)
	if title == "" {
		return text
	}
	pages := pageRange(selected.PageStart, selected.PageEnd)
	location := title
	if author := humanField(selected.Author); author != "" {
		location += " by " + author
	}
	if pages != "" {
		location += ", " + pages
	}
	return "This is described in " + location + ": " + text
}

func humanField(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) || unicode.Is(unicode.Cf, char)
	}), " ")
}

func pageRange(start, end uint32) string {
	if start == 0 && end == 0 {
		return ""
	}
	if end == 0 || end == start {
		return fmt.Sprintf("page %d", start)
	}
	return fmt.Sprintf("pages %d-%d", start, end)
}

func unsafeAnswerRune(value rune) bool {
	return unicode.IsControl(value) ||
		unicode.Is(unicode.Cf, value) ||
		value == '\u2028' ||
		value == '\u2029'
}

func summarizeSegments(values []domain.AnswerSegment, maximumSummaryRunes int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	summary := strings.Join(parts, " ")
	summary = strings.Join(strings.Fields(summary), " ")
	if utf8.RuneCountInString(summary) <= maximumSummaryRunes {
		return summary
	}
	runes := []rune(summary)
	return strings.TrimSpace(string(runes[:maximumSummaryRunes-1])) + "…"
}
