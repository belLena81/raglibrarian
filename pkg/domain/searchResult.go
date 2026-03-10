package domain

import (
	"github.com/google/uuid"
)

// SearchResult is a ranked answer returned for a Query, tying a passage to its Book.
type SearchResult struct {
	id      string
	queryId string
	book    Book
	chapter string
	pages   []int
	passage string
	score   float64
}

// NewSearchResult constructs a validated SearchResult.
func NewSearchResult(queryId string, book Book, chapter, passage string, pages []int, score float64) (SearchResult, error) {
	if err := validateQueryID(queryId); err != nil {
		return SearchResult{}, err
	}
	if err := validateChapter(chapter); err != nil {
		return SearchResult{}, err
	}
	if err := validatePassage(passage); err != nil {
		return SearchResult{}, err
	}
	if err := validatePages(pages); err != nil {
		return SearchResult{}, err
	}
	if err := validateScore(score); err != nil {
		return SearchResult{}, err
	}

	return SearchResult{
		id:      uuid.NewString(),
		queryId: queryId,
		book:    book,
		chapter: chapter,
		pages:   pages,
		passage: passage,
		score:   score,
	}, nil
}

// NewSearchResultFromDb reconstructs a SearchResult from persisted data, skipping validation.
// Only repository implementations should call this.
func NewSearchResultFromDb(id, queryId string, book Book, chapter, passage string, pages []int, score float64) SearchResult {
	return SearchResult{
		id:      id,
		queryId: queryId,
		book:    book,
		chapter: chapter,
		pages:   pages,
		passage: passage,
		score:   score,
	}
}

// Id returns the search result's unique identifier.
func (s SearchResult) Id() string { return s.id }

// QueryId returns the ID of the query this result belongs to.
func (s SearchResult) QueryId() string { return s.queryId }

// Book returns the book containing this result's passage.
func (s SearchResult) Book() Book { return s.book }

// Chapter returns the chapter name or heading for this passage.
func (s SearchResult) Chapter() string { return s.chapter }

// Pages returns the page numbers covered by this passage.
func (s SearchResult) Pages() []int { return s.pages }

// Passage returns the text excerpt relevant to the query.
func (s SearchResult) Passage() string { return s.passage }

// Score returns the relevance score in [0, 1].
func (s SearchResult) Score() float64 { return s.score }
