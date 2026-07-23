// epub_parser is invoked only through parser_sandbox. It parses one bounded
// EPUB archive and emits a private, bounded JSON-lines page stream.
package main

import (
	"os"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	pages, err := extractor.ParseEPUBFile(os.Args[1], extractor.DefaultEPUBArchiveLimits())
	if err != nil {
		os.Exit(3)
	}
	if err = extractor.WriteEPUBOutput(os.Stdout, pages); err != nil {
		os.Exit(4)
	}
}
