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
	sourcePath, limits, err := extractor.ParseEPUBParserArguments(arguments)
	if err != nil {
		traceEPUBParserInvalidArgs(arguments)
		_, _ = fmt.Fprintln(os.Stderr, "epub_parser_invalid_args")
		return 2
	}
	pages, err := extractor.ParseEPUBFile(sourcePath, limits)
	if err != nil {
		if detail, ok := extractor.EPUBParserFailureDetail(err); ok {
			_, _ = fmt.Fprintln(os.Stderr, detail) // #nosec G705 -- parser failure detail is bounded and emitted only on the local stderr trace path.
		}
		return extractor.EPUBParserExitCode(err)
	}
	if err = extractor.WriteEPUBOutput(output, pages); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "epub_parser_output_failed")
		return extractor.EPUBParserExitInternal
	}
	return 0
}

func traceEPUBParserInvalidArgs(arguments []string) {
	if strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")) == "" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "epub_parser_invalid_args_trace")
	_, _ = fmt.Fprintf(os.Stderr, "argc=%d env_source=%t\n", len(arguments), strings.TrimSpace(os.Getenv("EPUB_PARSER_SOURCE_PATH")) != "") // #nosec G705 -- trace-only diagnostics are gated by explicit opt-in env.
	_, _ = os.Stderr.Write(debug.Stack())
}

func traceEPUBParserEntry(arguments []string) {
	if strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")) == "" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "epub_parser_entry_trace")
	_, _ = fmt.Fprintf(os.Stderr, "argc=%d env_source=%t\n", len(arguments), strings.TrimSpace(os.Getenv("EPUB_PARSER_SOURCE_PATH")) != "") // #nosec G705 -- trace-only diagnostics are gated by explicit opt-in env.
}
