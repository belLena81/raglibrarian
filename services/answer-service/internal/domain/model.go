// Package domain contains Answer's transport-independent request and response model.
package domain

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidRequest = errors.New("invalid answer request")
	ErrForbidden      = errors.New("answer forbidden")
)

type RequestPolicy struct {
	MaximumQuestionCharacters int
	MaximumFilterTags         int
	MaximumTagCharacters      int
	MaximumAuthorCharacters   int
	MaximumResultLimit        uint32
}

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
	Question             string
	Filters              SearchFilters
	Limit                uint32
	Actor                Actor
	CorrelationID        string
	MinimumEvidenceScore float64
	// IncludeQueryMatchMetadata is controlled by Answer application policy and
	// is never accepted from the public Answer transport.
	IncludeQueryMatchMetadata bool
}

func (r SearchRequest) Validate(policy RequestPolicy) error {
	_, err := r.NormalizeAndValidate(policy)
	return err
}

func (r SearchRequest) NormalizeAndValidate(policy RequestPolicy) (SearchRequest, error) {
	if !r.Actor.CanAnswer() {
		return SearchRequest{}, ErrForbidden
	}
	if !validRequestPolicy(policy) {
		return SearchRequest{}, ErrInvalidRequest
	}
	if !utf8.ValidString(r.Question) ||
		utf8.RuneCountInString(r.Question) > policy.MaximumQuestionCharacters ||
		!utf8.ValidString(r.Filters.Author) ||
		utf8.RuneCountInString(r.Filters.Author) > policy.MaximumAuthorCharacters ||
		r.Limit > policy.MaximumResultLimit ||
		len(r.Filters.Tags) > policy.MaximumFilterTags ||
		!validCorrelationID(r.CorrelationID) {
		return SearchRequest{}, ErrInvalidRequest
	}
	if math.IsNaN(r.MinimumEvidenceScore) || math.IsInf(r.MinimumEvidenceScore, 0) || r.MinimumEvidenceScore < 0 || r.MinimumEvidenceScore > 1 {
		return SearchRequest{}, ErrInvalidRequest
	}
	if r.Filters.YearFrom != nil && (*r.Filters.YearFrom < 0 || *r.Filters.YearFrom > 9999) ||
		r.Filters.YearTo != nil && (*r.Filters.YearTo < 0 || *r.Filters.YearTo > 9999) ||
		r.Filters.YearFrom != nil && r.Filters.YearTo != nil && *r.Filters.YearFrom > *r.Filters.YearTo {
		return SearchRequest{}, ErrInvalidRequest
	}

	normalized := r
	normalized.Question = strings.TrimSpace(r.Question)
	normalized.Filters.Author = strings.TrimSpace(r.Filters.Author)
	normalized.Filters.Tags = make([]string, len(r.Filters.Tags))
	normalized.Filters.YearFrom = copyInt32(r.Filters.YearFrom)
	normalized.Filters.YearTo = copyInt32(r.Filters.YearTo)
	if normalized.Question == "" {
		return SearchRequest{}, ErrInvalidRequest
	}
	for index, tag := range r.Filters.Tags {
		if !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > policy.MaximumTagCharacters {
			return SearchRequest{}, ErrInvalidRequest
		}
		normalized.Filters.Tags[index] = strings.TrimSpace(tag)
		if normalized.Filters.Tags[index] == "" {
			return SearchRequest{}, ErrInvalidRequest
		}
	}
	return normalized, nil
}

func copyInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validRequestPolicy(policy RequestPolicy) bool {
	return policy.MaximumQuestionCharacters > 0 &&
		policy.MaximumFilterTags > 0 &&
		policy.MaximumTagCharacters > 0 &&
		policy.MaximumAuthorCharacters > 0 &&
		policy.MaximumResultLimit > 0
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
	Summary    string
}

type BookMetadata struct {
	BookID    string
	Title     string
	Author    string
	Year      int32
	Tags      []string
	MediaType string
}

type DocumentResult struct {
	DocumentID string
	Book       BookMetadata
	ChunkCount uint32
	PageStart  uint32
	PageEnd    uint32
	Score      float64
	Evidence   []Evidence
	Summary    string
}

type SearchResult struct {
	Query            string
	QueryEmbedding   []float32
	EmbeddingProfile string
	RetrievalProfile string
	CorpusSnapshot   string
	Results          []Evidence
	Documents        []DocumentResult
}

type ContextEvidence struct {
	EvidenceID string `json:"evidence_id"`
	Passage    string `json:"passage"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	Chapter    string `json:"chapter,omitempty"`
	Section    string `json:"section,omitempty"`
	PageStart  uint32 `json:"page_start,omitempty"`
	PageEnd    uint32 `json:"page_end,omitempty"`
}

type AnswerSegment struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type GroundedAnswer struct {
	Segments []AnswerSegment
}

type AnswerResult struct {
	Search  SearchResult
	Answer  *GroundedAnswer
	Summary string
}
