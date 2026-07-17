package catalog

import "testing"

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
