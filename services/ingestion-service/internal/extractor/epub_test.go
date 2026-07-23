package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
)

func TestParseEPUBReturnsSpineOrderAsLocations(t *testing.T) {
	path := writeSyntheticEPUB(t, []epubTestEntry{
		{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
		{name: "OPS/package.opf", contents: packageXML(
			`<item id="second" href="text/second.xhtml" media-type="application/xhtml+xml"/>`+
				`<item id="first" href="text/first.xhtml" media-type="application/xhtml+xml"/>`,
			`<itemref idref="first"/><itemref idref="second"/>`,
		)},
		{name: "OPS/text/first.xhtml", contents: xhtml("Chapter One", "First synthetic passage.")},
		{name: "OPS/text/second.xhtml", contents: xhtml("Chapter Two", "Second synthetic passage.")},
	})

	pages, err := ParseEPUBFile(path, EPUBArchiveLimits{
		MaximumEntries:       32,
		MaximumSpineItems:    8,
		MaximumEntryBytes:    1 << 20,
		MaximumExpandedBytes: 4 << 20,
		MaximumTextBytes:     1 << 20,
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Number != 1 || pages[1].Number != 2 {
		t.Fatalf("locations = %#v", pages)
	}
	if !strings.Contains(pages[0].Text, "Chapter One") || !strings.Contains(pages[1].Text, "Second synthetic passage.") {
		t.Fatalf("spine text = %#v", pages)
	}
}

func TestParseEPUBSkipsExplicitDirectoryEntries(t *testing.T) {
	path := writeSyntheticEPUB(t, []epubTestEntry{
		{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
		{name: "META-INF/", directory: true},
		{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
		{name: "OPS/", directory: true},
		{name: "OPS/package.opf", contents: packageXML(
			`<item id="chapter" href="text/chapter.xhtml" media-type="application/xhtml+xml"/>`,
			`<itemref idref="chapter"/>`,
		)},
		{name: "OPS/text/", directory: true},
		{name: "OPS/text/chapter.xhtml", contents: xhtml("Chapter One", "Directory entries are valid.")},
	})

	pages, err := ParseEPUBFile(path, DefaultEPUBArchiveLimits())

	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || !strings.Contains(pages[0].Text, "Directory entries are valid.") {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestParseEPUBAcceptsTrailingXMLWhitespaceInMetadata(t *testing.T) {
	path := writeSyntheticEPUB(t, []epubTestEntry{
		{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf") + "\n \t"},
		{name: "OPS/package.opf", contents: packageXML(
			`<item id="chapter" href="text/chapter.xhtml" media-type="application/xhtml+xml"/>`,
			`<itemref idref="chapter"/>`,
		) + "\n \t"},
		{name: "OPS/text/chapter.xhtml", contents: xhtml("Chapter One", "Trailing XML whitespace is valid.")},
	})

	pages, err := ParseEPUBFile(path, DefaultEPUBArchiveLimits())

	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || !strings.Contains(pages[0].Text, "Trailing XML whitespace is valid.") {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestParseEPUBAcceptsStandardHTMLDoctypeInXHTML(t *testing.T) {
	path := writeSyntheticEPUB(t, []epubTestEntry{
		{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
		{name: "OPS/package.opf", contents: packageXML(
			`<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`,
			`<itemref idref="chapter"/>`,
		)},
		{name: "OPS/chapter.xhtml", contents: `<?xml version="1.0"?>` +
			`<!DOCTYPE html>` +
			`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Standard doctype is valid EPUB XHTML.</p></body></html>`},
	})

	pages, err := ParseEPUBFile(path, DefaultEPUBArchiveLimits())

	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || !strings.Contains(pages[0].Text, "Standard doctype is valid EPUB XHTML.") {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestResolveEPUBReferenceAllowsEncodedSegmentsAndStripsFragments(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		href      string
		wantErr   bool
		want      string
	}{
		{
			name:      "encoded filename",
			directory: "OPS",
			href:      "Text/Chapter%201.xhtml",
			want:      "OPS/Text/Chapter 1.xhtml",
		},
		{
			name:      "fragment removed",
			directory: "OPS",
			href:      "chapter.xhtml#start",
			want:      "OPS/chapter.xhtml",
		},
		{
			name:      "invalid percent escape",
			directory: "OPS",
			href:      "chapter%ZZ.xhtml",
			wantErr:   true,
		},
		{
			name:      "decoded traversal rejected",
			directory: "OPS",
			href:      "Text/..%2Fsecret.xhtml",
			wantErr:   true,
		},
		{
			name:      "decoded slash inside segment rejected",
			directory: "OPS",
			href:      "chapter%2Fone.xhtml",
			wantErr:   true,
		},
		{
			name:      "absolute reference rejected",
			directory: "OPS",
			href:      "/chapter.xhtml",
			wantErr:   true,
		},
		{
			name:      "empty fragment rejected",
			directory: "OPS",
			href:      "#start",
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveEPUBReference(tc.directory, tc.href)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveEPUBReference() expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveEPUBReference() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveEPUBReference() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseEPUBRejectsUnsafeOrAmbiguousArchives(t *testing.T) {
	tests := []struct {
		name    string
		entries []epubTestEntry
	}{
		{
			name: "duplicate entry",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
			},
		},
		{
			name: "traversal rootfile",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: containerXML("../package.opf")},
			},
		},
		{
			name: "xml directive",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: `<!DOCTYPE container><container/>`},
			},
		},
		{
			name: "custom xhtml doctype",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
				{name: "OPS/package.opf", contents: packageXML(
					`<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`,
					`<itemref idref="chapter"/>`,
				)},
				{name: "OPS/chapter.xhtml", contents: `<?xml version="1.0"?>` +
					`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd">` +
					`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>custom doctype</p></body></html>`},
			},
		},
		{
			name: "xhtml entity declaration",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
				{name: "OPS/package.opf", contents: packageXML(
					`<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`,
					`<itemref idref="chapter"/>`,
				)},
				{name: "OPS/chapter.xhtml", contents: `<?xml version="1.0"?>` +
					`<!DOCTYPE html [<!ENTITY private "secret">]>` +
					`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>&private;</p></body></html>`},
			},
		},
		{
			name: "trailing container content",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf") + "trailing"},
			},
		},
		{
			name: "trailing package content",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
				{name: "OPS/package.opf", contents: packageXML(
					`<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`,
					`<itemref idref="chapter"/>`,
				) + "trailing"},
			},
		},
		{
			name: "traversal directory",
			entries: []epubTestEntry{
				{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
				{name: "../OPS/", directory: true},
				{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseEPUBFile(writeSyntheticEPUB(t, testCase.entries), DefaultEPUBArchiveLimits())
			category, categorized := FailureCategory(err)
			if err == nil || !categorized || category != domain.FailureMalformedDocument {
				t.Fatalf("ParseEPUBFile() error = %v, category = %q", err, category)
			}
		})
	}
}

func TestParseEPUBEnforcesExpandedByteLimit(t *testing.T) {
	path := writeSyntheticEPUB(t, []epubTestEntry{
		{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", contents: containerXML("OPS/package.opf")},
		{name: "OPS/package.opf", contents: packageXML(
			`<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`,
			`<itemref idref="chapter"/>`,
		)},
		{name: "OPS/chapter.xhtml", contents: xhtml("Chapter One", strings.Repeat("bounded ", 256))},
	})

	_, err := ParseEPUBFile(path, EPUBArchiveLimits{
		MaximumEntries:       16,
		MaximumSpineItems:    4,
		MaximumEntryBytes:    128,
		MaximumExpandedBytes: 512,
		MaximumTextBytes:     512,
	})
	category, categorized := FailureCategory(err)
	if !categorized || category != domain.FailureResourceLimitExceeded {
		t.Fatalf("ParseEPUBFile() error = %v, category = %q", err, category)
	}
}

func TestEPUBAdapterMapsParserOutputAndSanitizesFailure(t *testing.T) {
	runner := &epubRunnerStub{output: []byte(
		`{"schema_version":"v1","location_count":2}` + "\n" +
			`{"location":1,"text":"Chapter One\nSynthetic text."}` + "\n" +
			`{"location":2,"text":"Chapter Two\nMore text."}` + "\n",
	)}
	adapter := NewEPUB("/usr/local/bin/epub-parser", Limits{
		MaximumPages:          8,
		MaximumPageBytes:      1024,
		MaximumExtractedBytes: 4096,
	}, runner)
	var pages []Page
	info, err := adapter.Extract(context.Background(), "/tmp/source.epub", func(page Page) error {
		pages = append(pages, page)
		return nil
	})
	if err != nil || info.PageCount != 2 || len(pages) != 2 || pages[1].Number != 2 {
		t.Fatalf("Extract() = (%+v, %#v, %v)", info, pages, err)
	}
	if runner.path != "/usr/local/bin/epub-parser" || len(runner.args) != 1 || runner.args[0] != "/tmp/source.epub" {
		t.Fatalf("runner call = %q %#v", runner.path, runner.args)
	}

	runner.err = errors.New("private EPUB parser diagnostic")
	_, err = adapter.Extract(context.Background(), "/tmp/source.epub", func(Page) error { return nil })
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsanitized adapter error = %v", err)
	}
}

func TestEPUBParserExitProtocolPreservesFailureCategories(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		expected domain.FailureCategory
	}{
		{name: "malformed document", code: "125", expected: domain.FailureMalformedDocument},
		{name: "resource limit", code: "126", expected: domain.FailureResourceLimitExceeded},
		{name: "internal parser failure", code: "127", expected: domain.FailureInternalProcessing},
		{name: "sandbox resource limit", code: "121", expected: domain.FailureResourceLimitExceeded},
		{name: "sandbox setup", code: "122", expected: domain.FailureDependencyUnavailable},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := exec.Command("sh", "-c", "exit "+testCase.code).Run() // #nosec G204 -- fixed synthetic test input.
			classified := classifyEPUBCommandError(context.Background(), err)
			category, categorized := FailureCategory(classified)
			if !categorized || category != testCase.expected {
				t.Fatalf("classifyEPUBCommandError() category = %q, want %q", category, testCase.expected)
			}
			if strings.Contains(classified.Error(), "exit status") {
				t.Fatalf("classification exposed command diagnostics: %v", classified)
			}
		})
	}
}

func TestEPUBParserExitCodeUsesOnlyStableProtocolValues(t *testing.T) {
	tests := []struct {
		category domain.FailureCategory
		expected int
	}{
		{category: domain.FailureMalformedDocument, expected: EPUBParserExitMalformed},
		{category: domain.FailureResourceLimitExceeded, expected: EPUBParserExitResourceLimit},
		{category: domain.FailureInternalProcessing, expected: EPUBParserExitInternal},
	}
	for _, testCase := range tests {
		if actual := EPUBParserExitCode(epubFailure(testCase.category, errors.New("private diagnostic"))); actual != testCase.expected {
			t.Fatalf("EPUBParserExitCode(%q) = %d, want %d", testCase.category, actual, testCase.expected)
		}
	}
	if actual := EPUBParserExitCode(errors.New("uncategorized private diagnostic")); actual != EPUBParserExitInternal {
		t.Fatalf("EPUBParserExitCode(uncategorized) = %d, want %d", actual, EPUBParserExitInternal)
	}
}

type epubRunnerStub struct {
	path   string
	args   []string
	output []byte
	err    error
}

func (r *epubRunnerStub) Run(_ context.Context, path string, args []string, _ int64) ([]byte, error) {
	r.path = path
	r.args = append([]string(nil), args...)
	return append([]byte(nil), r.output...), r.err
}

type epubTestEntry struct {
	name      string
	contents  string
	method    uint16
	directory bool
}

func writeSyntheticEPUB(t *testing.T, entries []epubTestEntry) string {
	t.Helper()
	var contents bytes.Buffer
	writer := zip.NewWriter(&contents)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: entry.method}
		if entry.directory {
			header.SetMode(os.ModeDir | 0o755)
		}
		value, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = value.Write([]byte(entry.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "synthetic.epub")
	if err := os.WriteFile(path, contents.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containerXML(rootfile string) string {
	return `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">` +
		`<rootfiles><rootfile full-path="` + rootfile + `" media-type="application/oebps-package+xml"/></rootfiles></container>`
}

func packageXML(manifest, spine string) string {
	return `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0">` +
		`<manifest>` + manifest + `</manifest><spine>` + spine + `</spine></package>`
}

func xhtml(title, body string) string {
	return `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body><h1>` +
		title + `</h1><p>` + body + `</p></body></html>`
}
