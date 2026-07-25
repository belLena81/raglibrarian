// epub_parser is invoked only through parser_sandbox. It parses one bounded
// EPUB archive and emits a private, bounded JSON-lines page stream.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(arguments []string, output io.Writer) (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = fmt.Fprintln(os.Stderr, "epub_parser_panic")
			code = extractor.EPUBParserExitInternal
		}
	}()
	traceEPUBParserEntry(arguments)
	sourcePath := epubParserSourcePath(arguments)
	if sourcePath == "" {
		traceEPUBParserInvalidArgs(arguments)
		_, _ = fmt.Fprintln(os.Stderr, "epub_parser_invalid_args")
		return 2
	}
	pages, err := extractor.ParseEPUBFile(sourcePath, extractor.DefaultEPUBArchiveLimits())
	if err != nil {
		if detail, ok := extractor.EPUBParserFailureDetail(err); ok {
			_, _ = fmt.Fprintln(os.Stderr, detail)
		}
		return extractor.EPUBParserExitCode(err)
	}
	if err = extractor.WriteEPUBOutput(output, pages); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "epub_parser_output_failed")
		return extractor.EPUBParserExitInternal
	}
	return 0
}

func epubParserSourcePath(arguments []string) string {
	if len(arguments) > 0 {
		return strings.TrimSpace(arguments[len(arguments)-1])
	}
	return strings.TrimSpace(os.Getenv("EPUB_PARSER_SOURCE_PATH"))
}

func traceEPUBParserInvalidArgs(arguments []string) {
	if strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")) == "" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "epub_parser_invalid_args_trace")
	_, _ = fmt.Fprintf(os.Stderr, "argc=%d env_source=%t\n", len(arguments), strings.TrimSpace(os.Getenv("EPUB_PARSER_SOURCE_PATH")) != "")
	_, _ = os.Stderr.Write(debug.Stack())
}

func traceEPUBParserEntry(arguments []string) {
	if strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")) == "" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "epub_parser_entry_trace")
	_, _ = fmt.Fprintf(os.Stderr, "argc=%d env_source=%t\n", len(arguments), strings.TrimSpace(os.Getenv("EPUB_PARSER_SOURCE_PATH")) != "")
}
