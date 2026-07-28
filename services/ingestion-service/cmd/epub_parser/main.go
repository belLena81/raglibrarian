// epub_parser is invoked only through parser_sandbox. It parses one bounded
// EPUB archive and emits a private, bounded JSON-lines page stream.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
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
	limits := epubArchiveLimits()
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

func epubArchiveLimits() extractor.EPUBArchiveLimits {
	return extractor.EPUBArchiveLimits{
		MaximumEntries:       envInt("INGESTION_EPUB_MAX_ENTRIES", config.DefaultEPUBMaximumEntries, 3, config.MaximumEPUBMaximumEntries),
		MaximumSpineItems:    uint32(envInt64("INGESTION_EPUB_MAX_SPINE_ITEMS", config.DefaultEPUBMaximumSpineItems, 1, config.MaximumEPUBMaximumSpineItems)), // #nosec G115 -- bounded above.
		MaximumEntryBytes:    envInt64("INGESTION_EPUB_MAX_ENTRY_BYTES", config.DefaultEPUBMaximumEntryBytes, 1, config.MaximumEPUBMaximumEntryBytes),
		MaximumExpandedBytes: envInt64("INGESTION_EPUB_MAX_EXPANDED_BYTES", config.DefaultEPUBMaximumExpandedBytes, 1, config.MaximumEPUBMaximumExpandedBytes),
		MaximumTextBytes:     envInt64("INGESTION_EPUB_MAX_TEXT_BYTES", config.DefaultEPUBMaximumTextBytes, 1, config.MaximumEPUBMaximumTextBytes),
	}
}

func envInt(key string, fallback, minimum, maximum int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback, minimum, maximum int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return fallback
	}
	return parsed
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
