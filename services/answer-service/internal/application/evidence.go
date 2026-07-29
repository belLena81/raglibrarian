package application

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

type evidenceCandidate struct {
	evidence domain.Evidence
	group    string
}

func filterSearchByMinimumEvidenceScore(search domain.SearchResult, minimumEvidenceScore float64) domain.SearchResult {
	if minimumEvidenceScore <= 0 || math.IsNaN(minimumEvidenceScore) || math.IsInf(minimumEvidenceScore, 0) {
		return search
	}
	results := make([]domain.Evidence, 0, len(search.Results))
	for _, value := range search.Results {
		if value.Score >= minimumEvidenceScore {
			results = append(results, value)
		}
	}
	documents := make([]domain.DocumentResult, 0, len(search.Documents))
	for _, document := range search.Documents {
		evidence := make([]domain.Evidence, 0, len(document.Evidence))
		for _, value := range document.Evidence {
			if value.Score >= minimumEvidenceScore {
				evidence = append(evidence, value)
			}
		}
		if len(evidence) == 0 {
			continue
		}
		document.Evidence = evidence
		documents = append(documents, document)
	}
	search.Results = results
	search.Documents = documents
	return search
}

func selectEvidence(search domain.SearchResult, limits Limits) []domain.ContextEvidence {
	candidates := evidenceCandidates(search)
	selected := make([]domain.ContextEvidence, 0, limits.MaximumEvidence)
	seen := make(map[string]struct{})
	seenPassages := make(map[string]struct{})
	seenGroups := make(map[string]struct{})
	total := 0

	add := func(candidate evidenceCandidate, requireNewGroup bool) bool {
		contextValue := contextEvidence(candidate.evidence)
		if len(selected) >= limits.MaximumEvidence ||
			contextValue.EvidenceID == "" ||
			len(contextValue.Passage) == 0 ||
			len(contextValue.Passage) > limits.MaximumEvidenceBytes ||
			!validContextEvidence(contextValue) {
			return false
		}
		normalizedPassage := strings.Join(strings.Fields(contextValue.Passage), " ")
		if normalizedPassage == "" {
			return false
		}
		if candidate.group != "" {
			if _, found := seenGroups[candidate.group]; found && requireNewGroup {
				return false
			}
		}
		contextBytes := contextEvidenceBytes(contextValue)
		if _, found := seen[contextValue.EvidenceID]; found || total+contextBytes > limits.MaximumContextBytes {
			return false
		}
		if _, found := seenPassages[normalizedPassage]; found {
			return false
		}

		seen[contextValue.EvidenceID] = struct{}{}
		seenPassages[normalizedPassage] = struct{}{}
		if candidate.group != "" {
			seenGroups[candidate.group] = struct{}{}
		}
		total += contextBytes
		selected = append(selected, contextValue)
		return true
	}

	for _, candidate := range candidates {
		if len(selected) >= limits.MaximumEvidence {
			break
		}
		add(candidate, true)
	}
	if len(selected) >= limits.MaximumEvidence {
		return selected
	}
	for _, candidate := range candidates {
		if len(selected) >= limits.MaximumEvidence {
			break
		}
		add(candidate, false)
	}
	return selected
}

func evidenceCandidates(search domain.SearchResult) []evidenceCandidate {
	candidates := make([]evidenceCandidate, 0, len(search.Results))
	appendCandidate := func(value domain.Evidence) {
		candidates = append(candidates, evidenceCandidate{
			evidence: value,
			group:    evidenceDiversityGroup(value),
		})
	}
	for _, value := range search.Results {
		appendCandidate(value)
	}
	for _, document := range search.Documents {
		for _, value := range document.Evidence {
			appendCandidate(value)
		}
	}
	return candidates
}

func validContextEvidence(value domain.ContextEvidence) bool {
	return utf8.ValidString(value.EvidenceID) &&
		utf8.ValidString(value.Passage) &&
		utf8.ValidString(value.Title) &&
		utf8.ValidString(value.Author) &&
		utf8.ValidString(value.Chapter) &&
		utf8.ValidString(value.Section)
}

func contextEvidenceBytes(value domain.ContextEvidence) int {
	return len(value.EvidenceID) +
		len(value.Passage) +
		len(value.Title) +
		len(value.Author) +
		len(value.Chapter) +
		len(value.Section)
}

func evidenceDiversityGroup(value domain.Evidence) string {
	parts := []string{
		strings.TrimSpace(value.Book.BookID),
		strings.TrimSpace(value.Chapter),
		strings.TrimSpace(value.Section),
	}
	if parts[0] == "" {
		parts[0] = strings.TrimSpace(value.Book.Title)
	}
	return strings.Join(parts, "\x00")
}

func contextEvidence(value domain.Evidence) domain.ContextEvidence {
	return domain.ContextEvidence{
		EvidenceID: value.EvidenceID,
		Passage:    value.Passage,
		Title:      value.Book.Title,
		Author:     value.Book.Author,
		Chapter:    value.Chapter,
		Section:    value.Section,
		PageStart:  value.PageStart,
		PageEnd:    value.PageEnd,
	}
}
