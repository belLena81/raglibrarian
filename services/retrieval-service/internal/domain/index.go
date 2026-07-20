// Package domain contains Retrieval's transport-independent business model.
package domain

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaximumQuestionCharacters = 2000
	MaximumFilterTags         = 20
	MaximumTagCharacters      = 64
	MaximumAuthorCharacters   = 256
	DefaultResultLimit        = 5
	MaximumResultLimit        = 20
)

var (
	ErrInvalidSearchQuery = errors.New("invalid search query")
	ErrInvalidIndexJob    = errors.New("invalid index job")
	ErrTerminalIndexJob   = errors.New("index job is terminal")
)

// Actor is a live principal asserted by an authenticated Edge peer.
type Actor struct {
	UserID string
	Role   string
	Status string
}

// CanSearch authorizes every active product role without trusting browser data.
func (a Actor) CanSearch() bool {
	if a.UserID == "" || a.Status != "active" {
		return false
	}
	return a.Role == "reader" || a.Role == "librarian" || a.Role == "admin"
}

// SearchFilters are normalized metadata restrictions.
type SearchFilters struct {
	Tags     []string
	Author   string
	YearFrom *int
	YearTo   *int
}

// SearchQueryInput is untrusted input to the SearchQuery value object.
type SearchQueryInput struct {
	Question string
	Filters  SearchFilters
	Limit    int
}

// SearchQuery is a validated semantic-search request.
type SearchQuery struct {
	question string
	filters  SearchFilters
	limit    int
}

// NewSearchQuery validates and normalizes public query input.
func NewSearchQuery(input SearchQueryInput) (SearchQuery, error) {
	question := strings.TrimSpace(input.Question)
	if question == "" || !utf8.ValidString(question) || utf8.RuneCountInString(question) > MaximumQuestionCharacters {
		return SearchQuery{}, ErrInvalidSearchQuery
	}
	limit := input.Limit
	if limit == 0 {
		limit = DefaultResultLimit
	}
	if limit < 1 || limit > MaximumResultLimit || !validYear(input.Filters.YearFrom) || !validYear(input.Filters.YearTo) ||
		(input.Filters.YearFrom != nil && input.Filters.YearTo != nil && *input.Filters.YearFrom > *input.Filters.YearTo) ||
		len(input.Filters.Tags) > MaximumFilterTags {
		return SearchQuery{}, ErrInvalidSearchQuery
	}
	author := normalizeFilter(input.Filters.Author)
	if utf8.RuneCountInString(author) > MaximumAuthorCharacters {
		return SearchQuery{}, ErrInvalidSearchQuery
	}
	tags := make([]string, 0, len(input.Filters.Tags))
	seen := make(map[string]struct{}, len(input.Filters.Tags))
	for _, value := range input.Filters.Tags {
		tag := normalizeFilter(value)
		if tag == "" || utf8.RuneCountInString(tag) > MaximumTagCharacters {
			return SearchQuery{}, ErrInvalidSearchQuery
		}
		if _, found := seen[tag]; found {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return SearchQuery{
		question: question,
		filters:  SearchFilters{Tags: tags, Author: author, YearFrom: cloneInt(input.Filters.YearFrom), YearTo: cloneInt(input.Filters.YearTo)},
		limit:    limit,
	}, nil
}

func validYear(value *int) bool {
	return value == nil || *value >= 0
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func normalizeFilter(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func (q SearchQuery) Question() string { return q.question }
func (q SearchQuery) Filters() SearchFilters {
	return SearchFilters{Tags: append([]string(nil), q.filters.Tags...), Author: q.filters.Author, YearFrom: cloneInt(q.filters.YearFrom), YearTo: cloneInt(q.filters.YearTo)}
}
func (q SearchQuery) Limit() int { return q.limit }

// IndexJobState is Retrieval's indexing aggregate state.
type IndexJobState string

const (
	IndexJobPending IndexJobState = "pending"
	IndexJobIndexed IndexJobState = "indexed"
	IndexJobFailed  IndexJobState = "failed"
)

// FailureCategory is a stable sanitized indexing outcome.
type FailureCategory string

const (
	FailureManifestIntegrity      FailureCategory = "manifest_integrity"
	FailureIncompatibleProfile    FailureCategory = "incompatible_profile"
	FailureEmbeddingUnavailable   FailureCategory = "embedding_unavailable"
	FailureVectorStoreUnavailable FailureCategory = "vector_store_unavailable"
	FailureResourceLimit          FailureCategory = "resource_limit_exceeded"
	FailureIndexingTimeout        FailureCategory = "indexing_timeout"
	FailureInternalIndexing       FailureCategory = "internal_indexing_error"
)

// IndexJob owns the transition from a compatible manifest to an advertised index.
type IndexJob struct {
	id                string
	bookID            string
	sourceSHA256      [32]byte
	manifestSHA256    [32]byte
	profile           string
	expectedBatches   int
	completedBatchIDs map[string]struct{}
	state             IndexJobState
	failure           FailureCategory
	createdAt         time.Time
	updatedAt         time.Time
}

func NewIndexJob(id, bookID string, sourceSHA256, manifestSHA256 [32]byte, profile string, expectedBatches int, now time.Time) (IndexJob, error) {
	if !validIdentifier(id) || !validIdentifier(bookID) || profile == "" || expectedBatches < 1 || now.IsZero() || sourceSHA256 == ([32]byte{}) || manifestSHA256 == ([32]byte{}) {
		return IndexJob{}, ErrInvalidIndexJob
	}
	return IndexJob{id: id, bookID: bookID, sourceSHA256: sourceSHA256, manifestSHA256: manifestSHA256, profile: profile, expectedBatches: expectedBatches, completedBatchIDs: make(map[string]struct{}, expectedBatches), state: IndexJobPending, createdAt: now.UTC(), updatedAt: now.UTC()}, nil
}

// CompleteBatch records an idempotent batch fact and reports terminal success.
func (j *IndexJob) CompleteBatch(batchID string, now time.Time) (bool, error) {
	if j.state == IndexJobFailed {
		return false, ErrTerminalIndexJob
	}
	if j.state == IndexJobIndexed {
		return false, nil
	}
	if !validIdentifier(batchID) || now.IsZero() {
		return false, ErrInvalidIndexJob
	}
	if _, found := j.completedBatchIDs[batchID]; found {
		return false, nil
	}
	if len(j.completedBatchIDs) >= j.expectedBatches {
		return false, ErrInvalidIndexJob
	}
	j.completedBatchIDs[batchID] = struct{}{}
	j.updatedAt = now.UTC()
	if len(j.completedBatchIDs) == j.expectedBatches {
		j.state = IndexJobIndexed
		return true, nil
	}
	return false, nil
}

// Fail records one terminal sanitized failure.
func (j *IndexJob) Fail(category FailureCategory, now time.Time) (bool, error) {
	if j.state == IndexJobIndexed {
		return false, ErrTerminalIndexJob
	}
	if j.state == IndexJobFailed {
		if j.failure == category {
			return false, nil
		}
		return false, ErrTerminalIndexJob
	}
	if !validFailure(category) || now.IsZero() {
		return false, ErrInvalidIndexJob
	}
	j.state = IndexJobFailed
	j.failure = category
	j.updatedAt = now.UTC()
	return true, nil
}

func validFailure(category FailureCategory) bool {
	switch category {
	case FailureManifestIntegrity, FailureIncompatibleProfile, FailureEmbeddingUnavailable,
		FailureVectorStoreUnavailable, FailureResourceLimit, FailureIndexingTimeout, FailureInternalIndexing:
		return true
	default:
		return false
	}
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func (j IndexJob) ID() string               { return j.id }
func (j IndexJob) BookID() string           { return j.bookID }
func (j IndexJob) State() IndexJobState     { return j.state }
func (j IndexJob) Failure() FailureCategory { return j.failure }
func (j IndexJob) ExpectedBatches() int     { return j.expectedBatches }
func (j IndexJob) CompletedBatches() int    { return len(j.completedBatchIDs) }
func (j IndexJob) UpdatedAt() time.Time     { return j.updatedAt }
