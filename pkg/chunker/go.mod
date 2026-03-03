// Exportable recursive text splitter with token-aware chunking.
// Used by the ingest Lambda and independently testable.

module github.com/belLena81/raglibrarian/pkg/chunker

go 1.26

require (
    github.com/pkoukk/tiktoken-go   v0.1.7    // token counting for chunk size enforcement
    github.com/tmc/langchaingo      v0.1.13   // textsplitter: recursive character + sentence splitters
)