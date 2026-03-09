package domain

import (
	"github.com/google/uuid"
)

// SearchResult represents a single ranked answer returned for a Query.
// It ties a Chunk back to its Book with a relevance score.
type SearchResult struct {
	id      string
	queryId string
	book    Book
	chapter string
	pages   []int
	passage string
	score   float64
}

// NewSearchResult creates a SearchResult, returning an error if any field is invalid.
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

func (s SearchResult) Id() string      { return s.id }
func (s SearchResult) QueryId() string { return s.queryId }
func (s SearchResult) Book() Book      { return s.book }
func (s SearchResult) Chapter() string { return s.chapter }
func (s SearchResult) Pages() []int    { return s.pages }
func (s SearchResult) Passage() string { return s.passage }
func (s SearchResult) Score() float64  { return s.score }
