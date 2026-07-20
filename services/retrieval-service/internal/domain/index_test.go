package domain

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestSupportedIndexProfileDigestIsStable(t *testing.T) {
	profile := SupportedIndexProfile()
	if profile.Model != "jinaai/jina-embeddings-v2-base-code" || profile.Dimensions != 768 || profile.Distance != "cosine" {
		t.Fatalf("unexpected supported profile: %#v", profile)
	}
	if got := hex.EncodeToString(profile.Digest[:]); got != "096058de661aa9d81b4d8f9bd5005a613a951f9afcaf69400ecbb8079ab27740" {
		t.Fatalf("profile digest = %s", got)
	}
}

func TestActorCanSearchRequiresActiveKnownRole(t *testing.T) {
	for _, role := range []string{"reader", "librarian", "admin"} {
		if !(Actor{UserID: "user-1", Role: role, Status: "active"}).CanSearch() {
			t.Fatalf("active %s must be able to search", role)
		}
	}
	for _, actor := range []Actor{
		{},
		{UserID: "user-1", Role: "reader", Status: "pending"},
		{UserID: "user-1", Role: "unknown", Status: "active"},
	} {
		if actor.CanSearch() {
			t.Fatalf("unexpected search authorization for %#v", actor)
		}
	}
}

func TestNewSearchQueryNormalizesAndBoundsInput(t *testing.T) {
	yearFrom := 2020
	yearTo := 2026
	query, err := NewSearchQuery(SearchQueryInput{
		Question: "  How does replication work?  ",
		Filters: SearchFilters{
			Tags:     []string{" Databases ", "distributed-systems", "databases"},
			Author:   " Example Author ",
			YearFrom: &yearFrom,
			YearTo:   &yearTo,
		},
	})
	if err != nil {
		t.Fatalf("NewSearchQuery() error = %v", err)
	}
	if query.Question() != "How does replication work?" || query.Limit() != DefaultResultLimit {
		t.Fatalf("unexpected normalized query: %#v", query)
	}
	filters := query.Filters()
	if filters.Author != "example author" || len(filters.Tags) != 2 || filters.Tags[0] != "databases" {
		t.Fatalf("unexpected normalized filters: %#v", filters)
	}
	if filters.YearFrom == nil || filters.YearTo == nil || *filters.YearFrom != 2020 || *filters.YearTo != 2026 {
		t.Fatalf("unexpected normalized year filters: %#v", filters)
	}
}

func TestNewSearchQueryRejectsInvalidBounds(t *testing.T) {
	negativeYear := -1
	yearFrom := 2026
	yearTo := 2020
	tests := []SearchQueryInput{
		{},
		{Question: strings.Repeat("a", MaximumQuestionCharacters+1)},
		{Question: "valid", Limit: MaximumResultLimit + 1},
		{Question: "valid", Filters: SearchFilters{YearFrom: &negativeYear}},
		{Question: "valid", Filters: SearchFilters{YearFrom: &yearFrom, YearTo: &yearTo}},
		{Question: "valid", Filters: SearchFilters{Tags: make([]string, MaximumFilterTags+1)}},
		{Question: "valid", Filters: SearchFilters{Author: strings.Repeat("a", MaximumAuthorCharacters+1)}},
	}
	for index, input := range tests {
		if _, err := NewSearchQuery(input); err != ErrInvalidSearchQuery {
			t.Fatalf("case %d error = %v, want ErrInvalidSearchQuery", index, err)
		}
	}
}

func TestIndexJobCompletesOnlyAfterEveryBatch(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	job, err := NewIndexJob("job-1", "book-1", checksum(1), checksum(2), "m5-jina-code-v1", 2, now)
	if err != nil {
		t.Fatalf("NewIndexJob() error = %v", err)
	}
	if complete, err := job.CompleteBatch("batch-1", now.Add(time.Second)); err != nil || complete {
		t.Fatalf("first CompleteBatch() = %v, %v", complete, err)
	}
	if complete, err := job.CompleteBatch("batch-1", now.Add(2*time.Second)); err != nil || complete {
		t.Fatalf("duplicate CompleteBatch() = %v, %v", complete, err)
	}
	if complete, err := job.CompleteBatch("batch-2", now.Add(3*time.Second)); err != nil || !complete {
		t.Fatalf("last CompleteBatch() = %v, %v", complete, err)
	}
	if job.State() != IndexJobIndexed {
		t.Fatalf("state = %q, want indexed", job.State())
	}
}

func TestIndexJobFailureIsTerminalAndIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	job, err := NewIndexJob("job-1", "book-1", checksum(1), checksum(2), "m5-jina-code-v1", 1, now)
	if err != nil {
		t.Fatalf("NewIndexJob() error = %v", err)
	}
	if changed, err := job.Fail(FailureEmbeddingUnavailable, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("Fail() = %v, %v", changed, err)
	}
	if changed, err := job.Fail(FailureEmbeddingUnavailable, now.Add(2*time.Second)); err != nil || changed {
		t.Fatalf("duplicate Fail() = %v, %v", changed, err)
	}
	if _, err := job.CompleteBatch("batch-1", now.Add(3*time.Second)); err != ErrTerminalIndexJob {
		t.Fatalf("CompleteBatch() error = %v, want ErrTerminalIndexJob", err)
	}
}

func checksum(value byte) [32]byte {
	var result [32]byte
	result[0] = value
	return result
}
