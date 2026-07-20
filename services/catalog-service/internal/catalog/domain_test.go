package catalog

import (
	"testing"
	"time"
)

func TestBookTransitionToPermitsOnlyDocumentedLifecycle(t *testing.T) {
	tests := []struct {
		name    string
		current BookStatus
		next    BookStatus
		valid   bool
	}{
		{"pending processing", BookStatusPending, BookStatusProcessing, true},
		{"processing indexed", BookStatusProcessing, BookStatusIndexed, true},
		{"processing failed", BookStatusProcessing, BookStatusFailed, true},
		{"indexed reindexing", BookStatusIndexed, BookStatusReindexing, true},
		{"reindexing indexed", BookStatusReindexing, BookStatusIndexed, true},
		{"reindexing failed", BookStatusReindexing, BookStatusFailed, true},
		{"pending deleting", BookStatusPending, BookStatusDeleting, true},
		{"failed deleting", BookStatusFailed, BookStatusDeleting, true},
		{"deleting deleted", BookStatusDeleting, BookStatusDeleted, true},
		{"pending indexed", BookStatusPending, BookStatusIndexed, false},
		{"deleted deleting", BookStatusDeleted, BookStatusDeleting, false},
		{"indexed deleted", BookStatusIndexed, BookStatusDeleted, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			book := Book{ProcessingStatus: test.current}
			err := book.TransitionTo(test.next)
			if test.valid && err != nil {
				t.Fatalf("TransitionTo() error = %v", err)
			}
			if !test.valid && err != ErrInvalidTransition {
				t.Fatalf("TransitionTo() error = %v, want %v", err, ErrInvalidTransition)
			}
		})
	}
}

func TestBookAppliesProcessingFactsMonotonically(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	book := Book{
		ProcessingStatus:  BookStatusPending,
		ProcessingStage:   BookStageQueued,
		ProcessingVersion: 1,
	}
	changed, err := book.ApplyProcessingFact(ProcessingFact{Kind: ProcessingStarted, OccurredAt: now})
	if err != nil || !changed || book.ProcessingStatus != BookStatusProcessing || book.ProcessingStage != BookStageExtracting || book.ProcessingVersion != 2 {
		t.Fatalf("started = (%+v, %v, %v)", book, changed, err)
	}
	changed, err = book.ApplyProcessingFact(ProcessingFact{Kind: ProcessingChunksReady, OccurredAt: now.Add(time.Second)})
	if err != nil || !changed || book.ProcessingStage != BookStageChunksReady || book.ProcessingVersion != 3 {
		t.Fatalf("ready = (%+v, %v, %v)", book, changed, err)
	}
	changed, err = book.ApplyProcessingFact(ProcessingFact{Kind: ProcessingStarted, OccurredAt: now.Add(2 * time.Second)})
	if err != nil || changed || book.ProcessingStage != BookStageChunksReady || book.ProcessingVersion != 3 {
		t.Fatalf("late started = (%+v, %v, %v)", book, changed, err)
	}
}

func TestBookAppliesFastFailureFromPending(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	book := Book{ProcessingStatus: BookStatusPending, ProcessingStage: BookStageQueued, ProcessingVersion: 1}
	changed, err := book.ApplyProcessingFact(ProcessingFact{
		Kind:            ProcessingFailed,
		FailureCategory: FailureMalformedDocument,
		OccurredAt:      now,
	})
	if err != nil || !changed || book.ProcessingStatus != BookStatusFailed || book.ProcessingStage != BookStageFailed || book.ProcessingVersion != 2 {
		t.Fatalf("failed = (%+v, %v, %v)", book, changed, err)
	}
	if book.ProcessingFailureCategory != FailureMalformedDocument {
		t.Fatalf("failure category = %q", book.ProcessingFailureCategory)
	}
}

func TestBookRejectsContradictoryTerminalProcessingFacts(t *testing.T) {
	book := Book{ProcessingStatus: BookStatusFailed, ProcessingStage: BookStageFailed, ProcessingVersion: 3, ProcessingFailureCategory: FailureMalformedDocument}
	if _, err := book.ApplyProcessingFact(ProcessingFact{Kind: ProcessingChunksReady, OccurredAt: time.Now()}); err != ErrConflictingProcessingFact {
		t.Fatalf("ready after failure error = %v", err)
	}
}

func TestBookAppliesRetrievalTerminalFactsFromChunksReady(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	t.Run("indexed", func(t *testing.T) {
		book := Book{
			ProcessingStatus:  BookStatusProcessing,
			ProcessingStage:   BookStageChunksReady,
			ProcessingVersion: 3,
		}
		changed, err := book.ApplyProcessingFact(ProcessingFact{Kind: ProcessingIndexed, OccurredAt: now})
		if err != nil || !changed || book.ProcessingStatus != BookStatusIndexed ||
			book.ProcessingStage != BookStageIndexed || book.ProcessingVersion != 4 ||
			!book.ProcessingUpdatedAt.Equal(now) {
			t.Fatalf("indexed = (%+v, %v, %v)", book, changed, err)
		}
		changed, err = book.ApplyProcessingFact(ProcessingFact{Kind: ProcessingIndexed, OccurredAt: now.Add(time.Second)})
		if err != nil || changed || book.ProcessingVersion != 4 {
			t.Fatalf("duplicate indexed = (%+v, %v, %v)", book, changed, err)
		}
	})

	t.Run("indexing failed", func(t *testing.T) {
		book := Book{
			ProcessingStatus:  BookStatusProcessing,
			ProcessingStage:   BookStageChunksReady,
			ProcessingVersion: 3,
		}
		changed, err := book.ApplyProcessingFact(ProcessingFact{
			Kind:            ProcessingIndexingFailed,
			FailureCategory: FailureVectorStoreUnavailable,
			OccurredAt:      now,
		})
		if err != nil || !changed || book.ProcessingStatus != BookStatusFailed ||
			book.ProcessingStage != BookStageFailed || book.ProcessingVersion != 4 ||
			book.ProcessingFailureCategory != FailureVectorStoreUnavailable {
			t.Fatalf("indexing failed = (%+v, %v, %v)", book, changed, err)
		}
	})
}

func TestBookRejectsContradictoryAndPrematureRetrievalFacts(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		book Book
		fact ProcessingFact
	}{
		{
			name: "indexed before chunks ready",
			book: Book{ProcessingStatus: BookStatusProcessing, ProcessingStage: BookStageExtracting, ProcessingVersion: 2},
			fact: ProcessingFact{Kind: ProcessingIndexed, OccurredAt: now},
		},
		{
			name: "index failure before chunks ready",
			book: Book{ProcessingStatus: BookStatusProcessing, ProcessingStage: BookStageExtracting, ProcessingVersion: 2},
			fact: ProcessingFact{Kind: ProcessingIndexingFailed, FailureCategory: FailureInternalIndexingError, OccurredAt: now},
		},
		{
			name: "failure after indexed",
			book: Book{ProcessingStatus: BookStatusIndexed, ProcessingStage: BookStageIndexed, ProcessingVersion: 4},
			fact: ProcessingFact{Kind: ProcessingIndexingFailed, FailureCategory: FailureInternalIndexingError, OccurredAt: now},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if changed, err := test.book.ApplyProcessingFact(test.fact); err != ErrConflictingProcessingFact || changed {
				t.Fatalf("ApplyProcessingFact() = %v, %v", changed, err)
			}
		})
	}
}

func TestBookIgnoresStaleIngestionFactsAfterRetrievalTerminalState(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for _, book := range []Book{
		{ProcessingStatus: BookStatusIndexed, ProcessingStage: BookStageIndexed, ProcessingVersion: 4},
		{ProcessingStatus: BookStatusFailed, ProcessingStage: BookStageFailed, ProcessingVersion: 4, ProcessingFailureCategory: FailureIndexingTimeout},
	} {
		for _, fact := range []ProcessingFact{
			{Kind: ProcessingStarted, OccurredAt: now},
			{Kind: ProcessingChunksReady, OccurredAt: now},
			{Kind: ProcessingFailed, FailureCategory: FailureInternalProcessingError, OccurredAt: now},
		} {
			copy := book
			if changed, err := copy.ApplyProcessingFact(fact); err != nil || changed || copy.ProcessingVersion != 4 {
				t.Fatalf("stale fact %+v on %+v = %v, %v", fact, book, changed, err)
			}
		}
	}
}
