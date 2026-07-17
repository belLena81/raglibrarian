package logger

import "github.com/belLena81/raglibrarian/pkg/logger/safe"

// MaskedEmail is a display-only email value. Its String method never returns
// a submitted address verbatim and is safe for activity-log templates.
type MaskedEmail = safe.MaskedEmail

// BookSummary is the bounded book representation permitted in activity logs.
// It excludes tags, object references, checksums, events, and document data.
type BookSummary = safe.BookSummary
