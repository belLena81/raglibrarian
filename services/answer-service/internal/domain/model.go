// Package domain contains Answer's transport-independent request and response model.
package domain

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaximumQuestionCharacters = 2000
	MaximumFilterTags         = 20
	MaximumTagCharacters      = 64
	MaximumAuthorCharacters   = 256
	MaximumResultLimit        = 20
)

var (
	ErrInvalidRequest = errors.New("invalid answer request")
	ErrForbidden      = errors.New("answer forbidden")
)

type Actor struct {
	UserID string
	Role   string
	Status string
}

func (a Actor) CanAnswer() bool {
	return a.UserID != "" && a.Status == "active" && (a.Role == "reader" || a.Role == "librarian" || a.Role == "admin")
}

type SearchFilters struct {
	Tags     []string
	Author   string
	YearFrom *int32
	YearTo   *int32
}

type SearchRequest struct {
	Question      string
	Filters       SearchFilters
	Limit         uint32
	Actor         Actor
	CorrelationID string
}

func (r SearchRequest) Validate() error {
	if !r.Actor.CanAnswer() {
		return ErrForbidden
	}
	question := strings.TrimSpace(r.Question)
	if question == "" || !utf8.ValidString(question) || utf8.RuneCountInString(question) > MaximumQuestionCharacters || r.Limit > MaximumResultLimit ||
		len(r.Filters.Tags) > MaximumFilterTags || utf8.RuneCountInString(strings.TrimSpace(r.Filters.Author)) > MaximumAuthorCharacters || !validCorrelationID(r.CorrelationID) {
		return ErrInvalidRequest
	}
	if !utf8.ValidString(r.Filters.Author) || r.Filters.YearFrom != nil && (*r.Filters.YearFrom < 0 || *r.Filters.YearFrom > 9999) ||
		r.Filters.YearTo != nil && (*r.Filters.YearTo < 0 || *r.Filters.YearTo > 9999) ||
		r.Filters.YearFrom != nil && r.Filters.YearTo != nil && *r.Filters.YearFrom > *r.Filters.YearTo {
		return ErrInvalidRequest
	}
	for _, tag := range r.Filters.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > MaximumTagCharacters {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validCorrelationID(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

type Evidence struct {
	EvidenceID string
	ChunkID    string
	Book       BookMetadata
	Chapter    string
	Section    string
	PageStart  uint32
	PageEnd    uint32
	Passage    string
	Score      float64
}

type BookMetadata struct {
	BookID string
	Title  string
	Author string
	Year   int32
	Tags   []string
}

type DocumentResult struct {
	DocumentID string
	Book       BookMetadata
	ChunkCount uint32
	PageStart  uint32
	PageEnd    uint32
	Score      float64
	Evidence   []Evidence
}

type SearchResult struct {
	Query     string
	Results   []Evidence
	Documents []DocumentResult
}

type ContextEvidence struct {
	EvidenceID string `json:"evidence_id"`
	Passage    string `json:"passage"`
}

type AnswerSegment struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type GroundedAnswer struct {
	Segments []AnswerSegment
}

type AnswerResult struct {
	Search SearchResult
	Answer *GroundedAnswer
}
