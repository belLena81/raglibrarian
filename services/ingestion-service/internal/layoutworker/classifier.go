package layoutworker

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/layout"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/selection"
)

var (
	protectedHeading = regexp.MustCompile(`(?i)^(chapter\b|part\b|prologue\b|epilogue\b|introduction\b|preface\b|foreword\b|abstract\b)`)
	tocHeading       = regexp.MustCompile(`(?i)^(table of contents|contents)$`)
	figuresHeading   = regexp.MustCompile(`(?i)^list of (figures|tables|illustrations)$`)
	indexHeading     = regexp.MustCompile(`(?i)^(general )?index$`)
	copyrightText    = regexp.MustCompile(`(?i)(copyright|all rights reserved|isbn\b|published by)`)
	catalogHeading   = regexp.MustCompile(`(?i)^(other books|books by|also available|from the publisher)$`)
	alsoByHeading    = regexp.MustCompile(`(?i)^also by\b`)
	colophonHeading  = regexp.MustCompile(`(?i)^(colophon|typeface|printed (and bound )?by)`)
	dedicationText   = regexp.MustCompile(`(?i)^(for|to)\s+[[:alpha:]][[:alpha:] .'’-]{0,80}$`)
)

// Classify deliberately emits only conservative whole-location candidates.
// Text and coordinates are inspected locally and never returned from this boundary.
func Classify(document layout.Document) []selection.Candidate {
	if uint64(len(document.Locations)) > uint64(^uint32(0)) {
		return nil
	}
	result := make([]selection.Candidate, 0, len(document.Locations))
	total := uint32(len(document.Locations)) // #nosec G115 -- explicitly bounded above.
	for _, location := range document.Locations {
		if candidate, ok := classifyLocation(location, total); ok {
			result = append(result, candidate)
		}
	}
	return result
}

func classifyLocation(location layout.Location, total uint32) (selection.Candidate, bool) {
	texts := make([]string, 0, len(location.Items))
	var heading, titleLabel, documentIndex, furniture, picture bool
	var bodyWords, bodyBlocks int
	for _, item := range location.Items {
		text := strings.TrimSpace(item.Text)
		if text != "" {
			texts = append(texts, text)
		}
		switch item.Label {
		case "title":
			titleLabel = true
			heading = true
		case "section_header":
			heading = true
		case "document_index":
			documentIndex = true
		case "picture", "caption":
			picture = true
		}
		if item.ContentLayer == "furniture" {
			furniture = true
		}
		if item.ContentLayer == "body" && (item.Label == "paragraph" || item.Label == "text" || item.Label == "list_item") {
			words := wordCount(text)
			bodyWords += words
			if words >= 12 {
				bodyBlocks++
			}
		}
	}
	joined := strings.TrimSpace(strings.Join(texts, "\n"))
	first := ""
	if len(texts) > 0 {
		first = texts[0]
	}
	protected := protectedHeading.MatchString(first)
	mixedContent := bodyWords >= 180 || bodyBlocks >= 3
	keep := protected || mixedContent
	early := location.Ordinal <= 12
	late := total > 0 && location.Ordinal+12 > total
	sparse := bodyWords <= 80 && len(location.Items) <= 24

	makeCandidate := func(reason selection.Reason, signals ...selection.Signal) (selection.Candidate, bool) {
		return selection.Candidate{Ordinal: location.Ordinal, Reason: reason, Signals: signals, Keep: keep}, true
	}
	switch {
	case tocHeading.MatchString(first) && documentIndex && early:
		return makeCandidate(selection.ReasonTableOfContents, selection.SignalHierarchy, selection.SignalLayout, selection.SignalPosition)
	case figuresHeading.MatchString(first) && documentIndex:
		return makeCandidate(selection.ReasonListOfFiguresTables, selection.SignalHierarchy, selection.SignalLayout)
	case indexHeading.MatchString(first) && documentIndex && late:
		return makeCandidate(selection.ReasonIndex, selection.SignalHierarchy, selection.SignalLayout, selection.SignalPosition)
	case copyrightText.MatchString(joined) && early && sparse:
		return makeCandidate(selection.ReasonCopyright, selection.SignalContentShape, selection.SignalPosition, selection.SignalLayout)
	case alsoByHeading.MatchString(first) && (early || late) && sparse:
		return makeCandidate(selection.ReasonAlsoBy, selection.SignalHierarchy, selection.SignalPosition, selection.SignalLayout)
	case catalogHeading.MatchString(first) && (early || late) && sparse:
		return makeCandidate(selection.ReasonPublisherCatalog, selection.SignalHierarchy, selection.SignalPosition, selection.SignalLayout)
	case colophonHeading.MatchString(first) && late && sparse:
		return makeCandidate(selection.ReasonColophon, selection.SignalHierarchy, selection.SignalPosition, selection.SignalLayout)
	case dedicationText.MatchString(first) && early && sparse && (picture || heading):
		return makeCandidate(selection.ReasonDedicationOrnamental, selection.SignalContentShape, selection.SignalPosition, selection.SignalLayout)
	case titleLabel && early && sparse && len(texts) <= 4 && !protected:
		return makeCandidate(selection.ReasonTitle, selection.SignalHierarchy, selection.SignalPosition, selection.SignalLayout)
	case furniture && sparse && (early || late) && copyrightText.MatchString(joined):
		return makeCandidate(selection.ReasonCopyright, selection.SignalLayout, selection.SignalContentShape, selection.SignalPosition)
	default:
		return selection.Candidate{}, false
	}
}

func wordCount(value string) int {
	return len(strings.FieldsFunc(value, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsNumber(char)
	}))
}
