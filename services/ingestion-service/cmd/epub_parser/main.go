// epub_parser is invoked only through parser_sandbox. It parses one bounded
// EPUB archive and emits a private, bounded JSON-lines page stream.
package main

import (
	"io"
	"os"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(arguments []string, output io.Writer) int {
	if len(arguments) != 1 {
		return 2
	}
	pages, err := extractor.ParseEPUBFile(arguments[0], extractor.DefaultEPUBArchiveLimits())
	if err != nil {
		return extractor.EPUBParserExitCode(err)
	}
	if err = extractor.WriteEPUBOutput(output, pages); err != nil {
		return extractor.EPUBParserExitInternal
	}
	return 0
}
