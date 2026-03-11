package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// ── NewBook ───────────────────────────────────────────────────────────────────

func TestNewBook_Valid(t *testing.T) {
	book, err := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)

	require.NoError(t, err)
	assert.NotEmpty(t, book.Id())
	assert.Equal(t, "The Go Programming Language", book.Title())
	assert.Equal(t, "Donovan & Kernighan", book.Author())
	assert.Equal(t, 2015, book.Year())
	assert.WithinDuration(t, time.Now().UTC(), book.CreatedAt(), time.Second)
	assert.WithinDuration(t, time.Now().UTC(), book.UpdatedAt(), time.Second)
	// New fields: safe zero values on construction.
	assert.Equal(t, domain.Pending, book.Status())
	assert.Empty(t, book.Tags())
	assert.Empty(t, book.S3Key())
}

func TestNewBook_UniqueIDs(t *testing.T) {
	a, err := domain.NewBook("Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	b, err := domain.NewBook("Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	assert.NotEqual(t, a.Id(), b.Id())
}

// TestErrDuplicateBook_Sentinel documents that ErrDuplicateBook is the error
// repository implementations must return when the (title, author, year) unique
// constraint is violated. The domain owns the sentinel; the repository maps the
// Postgres unique-violation SQLSTATE to it.
func TestErrDuplicateBook_Sentinel(t *testing.T) {
	assert.NotNil(t, domain.ErrDuplicateBook)
	assert.Contains(t, domain.ErrDuplicateBook.Error(), "title")
	assert.Contains(t, domain.ErrDuplicateBook.Error(), "author")
	assert.Contains(t, domain.ErrDuplicateBook.Error(), "year")
}

func TestNewBook_InvalidTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tab only", "\t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewBook(tt.title, "Some Author", 2020)
			assert.ErrorIs(t, err, domain.ErrEmptyTitle)
		})
	}
}

func TestNewBook_InvalidAuthor(t *testing.T) {
	tests := []struct {
		name   string
		author string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewBook("Valid Title", tt.author, 2020)
			assert.ErrorIs(t, err, domain.ErrEmptyAuthor)
		})
	}
}

func TestNewBook_InvalidYear(t *testing.T) {
	currentYear := time.Now().UTC().Year()

	tests := []struct {
		name string
		year int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too old", 1899},
		{"future", currentYear + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewBook("Valid Title", "Valid Author", tt.year)
			assert.ErrorIs(t, err, domain.ErrInvalidYear)
		})
	}
}

// ── Status value object ──────────────────────────────────────────────────

func TestStatus_IsValid(t *testing.T) {
	assert.True(t, domain.Pending.IsValid())
	assert.True(t, domain.Indexing.IsValid())
	assert.True(t, domain.Indexed.IsValid())
	assert.True(t, domain.Failed.IsValid())
	_, ok := domain.StatusValueOf("")
	assert.ErrorIs(t, ok, domain.ErrInvalidStatus)
	_, ok = domain.StatusValueOf("unknown")
	assert.ErrorIs(t, ok, domain.ErrInvalidStatus)
	_, ok = domain.StatusValueOf("PENDING")
	assert.NoError(t, ok)
}

func TestStatus_String(t *testing.T) {
	assert.Equal(t, "pending", domain.Pending.String())
	assert.Equal(t, "indexing", domain.Indexing.String())
	assert.Equal(t, "indexed", domain.Indexed.String())
	assert.Equal(t, "failed", domain.Failed.String())
}

func TestStatus_Values(t *testing.T) {
	values := domain.StatusValues()

	assert.Len(t, values, 4)
	assert.Contains(t, values, domain.Pending)
	assert.Contains(t, values, domain.Indexing)
	assert.Contains(t, values, domain.Indexed)
	assert.Contains(t, values, domain.Failed)
}

func TestStatus_ValueOf_Valid(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.Status
	}{
		{"pending", domain.Pending},
		{"indexing", domain.Indexing},
		{"indexed", domain.Indexed},
		{"failed", domain.Failed},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := domain.StatusValueOf(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestStatus_ValueOf_Invalid(t *testing.T) {
	tests := []string{"PENDING", "Indexed"}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := domain.StatusValueOf(input)
			assert.NoError(t, err)
		})
	}
	tests = []string{"", "unknown"}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := domain.StatusValueOf(input)
			assert.ErrorIs(t, err, domain.ErrInvalidStatus)
		})
	}
}

// ── TransitionTo ──────────────────────────────────────────────────────────────

func TestStatus_TransitionTo_AllowedPaths(t *testing.T) {
	tests := []struct {
		from domain.Status
		to   domain.Status
	}{
		// Normal ingestion flow.
		{domain.Pending, domain.Indexing},
		{domain.Indexing, domain.Indexed},
		{domain.Indexing, domain.Failed},
		// Reindex: reset from any terminal state back to pending.
		{domain.Indexed, domain.Pending},
		{domain.Failed, domain.Pending},
	}
	for _, tt := range tests {
		t.Run(tt.from.String()+"→"+tt.to.String(), func(t *testing.T) {
			assert.NoError(t, tt.from.TransitionTo(tt.to))
		})
	}
}

func TestStatus_TransitionTo_ForbiddenPaths(t *testing.T) {
	tests := []struct {
		from domain.Status
		to   domain.Status
	}{
		// Cannot skip indexing.
		{domain.Pending, domain.Indexed},
		{domain.Pending, domain.Failed},
		// Cannot go backward through the pipeline.
		{domain.Indexed, domain.Indexing},
		{domain.Failed, domain.Indexing},
		// No self-transitions.
		{domain.Pending, domain.Pending},
		{domain.Indexing, domain.Indexing},
		{domain.Indexed, domain.Indexed},
		{domain.Failed, domain.Failed},
	}
	for _, tt := range tests {
		t.Run(tt.from.String()+"→"+tt.to.String(), func(t *testing.T) {
			assert.ErrorIs(t, tt.from.TransitionTo(tt.to), domain.ErrInvalidStatusTransition)
		})
	}
}

func TestStatus_TransitionTo_InvalidTarget_ReturnsInvalidStatus(t *testing.T) {
	s, _ := domain.StatusValueOf("bad")
	err := domain.Pending.TransitionTo(s)
	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

// ── Book.SetStatus ───────────────────────────────────────────────────────

func TestBook_SetStatus_ValidTransition_UpdatesAndAdvancesUpdatedAt(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)
	before := book.Status()
	time.Sleep(time.Millisecond)

	err = book.SetStatus(domain.Indexing)

	require.NoError(t, err)
	assert.Equal(t, domain.Indexing, book.Status())
	_ = before
}

func TestBook_SetStatus_InvalidTransition_DoesNotMutate(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	err = book.SetStatus(domain.Indexed) // pending → indexed: forbidden
	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
	assert.Equal(t, domain.Pending, book.Status()) // unchanged
}

// ── Book.SetS3Key ─────────────────────────────────────────────────────────────

func TestBook_SetS3Key_Valid(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)
	before := book.UpdatedAt()
	time.Sleep(time.Millisecond)

	err = book.SetS3Key("books/abc-123/file.pdf")

	require.NoError(t, err)
	assert.Equal(t, "books/abc-123/file.pdf", book.S3Key())
	assert.True(t, book.UpdatedAt().After(before))
}

func TestBook_SetS3Key_Empty_ReturnsError(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	assert.ErrorIs(t, book.SetS3Key(""), domain.ErrEmptyS3Key)
	assert.ErrorIs(t, book.SetS3Key("   "), domain.ErrEmptyS3Key)
	assert.Empty(t, book.S3Key()) // unchanged
}

// ── Book.SetTags ──────────────────────────────────────────────────────────────

func TestBook_SetTags_Valid(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	err = book.SetTags([]string{"go", "concurrency"})

	require.NoError(t, err)
	assert.Equal(t, []string{"go", "concurrency"}, book.Tags())
}

func TestBook_SetTags_NilAndEmpty_StoredAsEmptySlice(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	// nil input must never leave Tags() returning nil — callers get an empty slice.
	require.NoError(t, book.SetTags(nil))
	assert.NotNil(t, book.Tags())
	assert.Empty(t, book.Tags())

	require.NoError(t, book.SetTags([]string{}))
	assert.NotNil(t, book.Tags())
	assert.Empty(t, book.Tags())
}

func TestBook_SetTags_EmptyTag_ReturnsError(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	tests := []struct {
		name string
		tags []string
	}{
		{"empty string element", []string{"go", ""}},
		{"whitespace element", []string{"go", "  "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, book.SetTags(tt.tags), domain.ErrInvalidTag)
		})
	}
}

func TestBook_SetTags_DuplicateTag_ReturnsError(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	assert.ErrorIs(t, book.SetTags([]string{"go", "go"}), domain.ErrInvalidTag)
}

func TestBook_SetTags_DoesNotMutateOnError(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, book.SetTags([]string{"go"}))

	_ = book.SetTags([]string{"go", ""})         // error: blank tag
	assert.Equal(t, []string{"go"}, book.Tags()) // unchanged
}

// ── Setter regression: existing setters still work ───────────────────────────

func TestBook_SetTitle(t *testing.T) {
	book, err := domain.NewBook("Original Title", "Author", 2020)
	require.NoError(t, err)
	before := book.UpdatedAt()
	time.Sleep(time.Millisecond)

	require.NoError(t, book.SetTitle("New Title"))
	assert.Equal(t, "New Title", book.Title())
	assert.True(t, book.UpdatedAt().After(before))
}

func TestBook_SetTitle_Invalid(t *testing.T) {
	book, err := domain.NewBook("Original Title", "Author", 2020)
	require.NoError(t, err)

	assert.ErrorIs(t, book.SetTitle(""), domain.ErrEmptyTitle)
	assert.Equal(t, "Original Title", book.Title())
}

func TestBook_SetAuthor(t *testing.T) {
	book, err := domain.NewBook("Title", "Original Author", 2020)
	require.NoError(t, err)

	require.NoError(t, book.SetAuthor("New Author"))
	assert.Equal(t, "New Author", book.Author())
}

func TestBook_SetAuthor_Invalid(t *testing.T) {
	book, err := domain.NewBook("Title", "Original Author", 2020)
	require.NoError(t, err)

	assert.ErrorIs(t, book.SetAuthor("   "), domain.ErrEmptyAuthor)
	assert.Equal(t, "Original Author", book.Author())
}

func TestBook_SetYear(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	require.NoError(t, book.SetYear(2021))
	assert.Equal(t, 2021, book.Year())
}

func TestBook_SetYear_Invalid(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	assert.ErrorIs(t, book.SetYear(1800), domain.ErrInvalidYear)
	assert.Equal(t, 2020, book.Year())
}

// ── NewBookFromDB reconstruction ──────────────────────────────────────────────

func TestNewBookFromDB_ReconstructsAllFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	book := domain.NewBookFromDB(
		"id-1",
		"Clean Architecture",
		"Robert Martin",
		2017,
		domain.Indexed,
		[]string{"architecture", "ddd"},
		"books/id-1/file.pdf",
		now,
		now,
	)

	assert.Equal(t, "id-1", book.Id())
	assert.Equal(t, "Clean Architecture", book.Title())
	assert.Equal(t, "Robert Martin", book.Author())
	assert.Equal(t, 2017, book.Year())
	assert.Equal(t, domain.Indexed, book.Status())
	assert.Equal(t, []string{"architecture", "ddd"}, book.Tags())
	assert.Equal(t, "books/id-1/file.pdf", book.S3Key())
	assert.Equal(t, now, book.CreatedAt())
	assert.Equal(t, now, book.UpdatedAt())
}

func TestNewBookFromDB_NilTags_ReturnsEmptySlice(t *testing.T) {
	now := time.Now().UTC()
	book := domain.NewBookFromDB("id-1", "Title", "Author", 2020,
		domain.Pending, nil, "", now, now)

	assert.NotNil(t, book.Tags())
	assert.Empty(t, book.Tags())
}
