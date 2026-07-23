package extractor

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
)

const EPUBExtractionVersion = "epub-spine-v1"

const (
	epubMediaType           = "application/epub+zip"
	epubPackageMediaType    = "application/oebps-package+xml"
	epubXHTMLMediaType      = "application/xhtml+xml"
	epubOutputSchemaVersion = "v1"
	maximumXMLDepth         = 128
	maximumXMLAttributes    = 64
	maximumXMLTokens        = 1_000_000

	// These form the private parser process protocol. Keep them separate
	// from parser_sandbox's reserved 121-124 exit statuses.
	EPUBParserExitMalformed     = 125
	EPUBParserExitResourceLimit = 126
	EPUBParserExitInternal      = 127
)

// EPUBArchiveLimits bounds every attacker-controlled archive dimension before
// extraction. The parser binary has an additional OS resource sandbox.
type EPUBArchiveLimits struct {
	MaximumEntries       int
	MaximumSpineItems    uint32
	MaximumEntryBytes    int64
	MaximumExpandedBytes int64
	MaximumTextBytes     int64
}

func DefaultEPUBArchiveLimits() EPUBArchiveLimits {
	return EPUBArchiveLimits{
		MaximumEntries:       2048,
		MaximumSpineItems:    500,
		MaximumEntryBytes:    32 << 20,
		MaximumExpandedBytes: 256 << 20,
		MaximumTextBytes:     128 << 20,
	}
}

type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
		Media    string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubPackage struct {
	Manifest []struct {
		ID    string `xml:"id,attr"`
		Href  string `xml:"href,attr"`
		Media string `xml:"media-type,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

// ParseEPUBFile parses a bounded EPUB archive into deterministic spine
// locations. Page numbers are EPUB spine ordinals; public presentation labels
// those ordinals as "Location".
func ParseEPUBFile(sourcePath string, limits EPUBArchiveLimits) ([]Page, error) {
	if !validEPUBLimits(limits) {
		return nil, epubFailure(domain.FailureInternalProcessing, errors.New("invalid EPUB limits"))
	}
	archive, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, epubFailure(domain.FailureMalformedDocument, err)
	}
	defer func() { _ = archive.Close() }()
	if len(archive.File) < 3 {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("incomplete EPUB archive"))
	}
	if len(archive.File) > limits.MaximumEntries {
		return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB entry limit exceeded"))
	}
	if archive.File[0].Name != "mimetype" || archive.File[0].Method != zip.Store {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB mimetype entry"))
	}

	files := make(map[string]*zip.File, len(archive.File))
	var declaredExpanded uint64
	for _, file := range archive.File {
		if file.Flags&1 != 0 {
			return nil, epubFailure(domain.FailureMalformedDocument, errors.New("unsafe EPUB entry"))
		}
		if file.FileInfo().IsDir() {
			if !validEPUBArchiveDirectory(file.Name) {
				return nil, epubFailure(domain.FailureMalformedDocument, errors.New("unsafe EPUB entry"))
			}
			continue
		}
		if !validEPUBArchivePath(file.Name) {
			return nil, epubFailure(domain.FailureMalformedDocument, errors.New("unsafe EPUB entry"))
		}
		if _, duplicate := files[file.Name]; duplicate {
			return nil, epubFailure(domain.FailureMalformedDocument, errors.New("duplicate EPUB entry"))
		}
		// #nosec G115 -- validEPUBLimits establishes that MaximumEntryBytes is positive.
		if file.UncompressedSize64 > uint64(limits.MaximumEntryBytes) {
			return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB entry limit exceeded"))
		}
		declaredExpanded += file.UncompressedSize64
		// #nosec G115 -- validEPUBLimits establishes that MaximumExpandedBytes is positive.
		if declaredExpanded > uint64(limits.MaximumExpandedBytes) {
			return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB expansion limit exceeded"))
		}
		files[file.Name] = file
	}

	var expanded int64
	mimetype, err := readEPUBEntry(files["mimetype"], limits.MaximumEntryBytes, limits.MaximumExpandedBytes, &expanded)
	if err != nil {
		return nil, err
	}
	if string(mimetype) != epubMediaType {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB media type"))
	}
	containerBytes, err := readEPUBEntry(files["META-INF/container.xml"], limits.MaximumEntryBytes, limits.MaximumExpandedBytes, &expanded)
	if err != nil {
		return nil, err
	}
	var container epubContainer
	if err = decodeStrictXML(containerBytes, &container); err != nil || len(container.Rootfiles) != 1 {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB container"))
	}
	rootfile := container.Rootfiles[0]
	if rootfile.Media != epubPackageMediaType || !validEPUBArchivePath(rootfile.FullPath) {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB package reference"))
	}
	packageBytes, err := readEPUBEntry(files[rootfile.FullPath], limits.MaximumEntryBytes, limits.MaximumExpandedBytes, &expanded)
	if err != nil {
		return nil, err
	}
	var publication epubPackage
	if err = decodeStrictXML(packageBytes, &publication); err != nil || len(publication.Manifest) == 0 ||
		len(publication.Spine) == 0 {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB package"))
	}
	if len(publication.Spine) > int(limits.MaximumSpineItems) {
		return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB spine limit exceeded"))
	}

	manifest := make(map[string]string, len(publication.Manifest))
	packageDirectory := path.Dir(rootfile.FullPath)
	for _, item := range publication.Manifest {
		if !validEPUBIdentifier(item.ID) || item.Media != epubXHTMLMediaType {
			continue
		}
		reference, resolveErr := resolveEPUBReference(packageDirectory, item.Href)
		if resolveErr != nil {
			return nil, epubFailure(domain.FailureMalformedDocument, resolveErr)
		}
		if _, duplicate := manifest[item.ID]; duplicate {
			return nil, epubFailure(domain.FailureMalformedDocument, errors.New("duplicate EPUB manifest ID"))
		}
		manifest[item.ID] = reference
	}

	pages := make([]Page, 0, len(publication.Spine))
	var textBytes int64
	seenSpine := make(map[string]struct{}, len(publication.Spine))
	for index, item := range publication.Spine {
		reference, found := manifest[item.IDRef]
		if !found {
			return nil, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB spine reference"))
		}
		if _, duplicate := seenSpine[reference]; duplicate {
			return nil, epubFailure(domain.FailureMalformedDocument, errors.New("duplicate EPUB spine reference"))
		}
		seenSpine[reference] = struct{}{}
		xhtmlBytes, readErr := readEPUBEntry(files[reference], limits.MaximumEntryBytes, limits.MaximumExpandedBytes, &expanded)
		if readErr != nil {
			return nil, readErr
		}
		text, parseErr := extractEPUBXHTML(xhtmlBytes, limits.MaximumTextBytes-textBytes)
		if parseErr != nil {
			return nil, parseErr
		}
		textBytes += int64(len(text))
		if textBytes > limits.MaximumTextBytes {
			return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB text limit exceeded"))
		}
		pages = append(pages, Page{Number: uint32(index + 1), Text: text}) // #nosec G115 -- bounded by MaximumSpineItems.
	}
	return pages, nil
}

func validEPUBLimits(limits EPUBArchiveLimits) bool {
	return limits.MaximumEntries >= 3 && limits.MaximumSpineItems > 0 && limits.MaximumEntryBytes > 0 &&
		limits.MaximumExpandedBytes >= limits.MaximumEntryBytes && limits.MaximumTextBytes > 0 &&
		limits.MaximumTextBytes <= limits.MaximumExpandedBytes
}

func validEPUBArchivePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) || strings.ContainsRune(value, 0) ||
		len(value) > 1024 || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validEPUBArchiveDirectory(value string) bool {
	return strings.HasSuffix(value, "/") && validEPUBArchivePath(strings.TrimSuffix(value, "/"))
}

func validEPUBIdentifier(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func resolveEPUBReference(directory, href string) (string, error) {
	if href == "" || strings.ContainsAny(href, `\?#%`) || strings.HasPrefix(href, "/") {
		return "", errors.New("invalid EPUB manifest reference")
	}
	for _, segment := range strings.Split(href, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("invalid EPUB manifest reference")
		}
	}
	reference := path.Join(directory, href)
	if !validEPUBArchivePath(reference) {
		return "", errors.New("invalid EPUB manifest reference")
	}
	return reference, nil
}

func readEPUBEntry(file *zip.File, maximumEntry, maximumExpanded int64, expanded *int64) ([]byte, error) {
	if file == nil {
		return nil, epubFailure(domain.FailureMalformedDocument, errors.New("missing EPUB entry"))
	}
	if maximumEntry <= 0 || maximumExpanded < maximumEntry {
		return nil, epubFailure(domain.FailureInternalProcessing, errors.New("invalid EPUB entry limits"))
	}
	// #nosec G115 -- the limits are checked as positive immediately above.
	if file.UncompressedSize64 > uint64(maximumEntry) {
		return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB entry limit exceeded"))
	}
	reader, err := file.Open()
	if err != nil {
		return nil, epubFailure(domain.FailureMalformedDocument, err)
	}
	defer func() { _ = reader.Close() }()
	contents, err := io.ReadAll(io.LimitReader(reader, maximumEntry+1))
	if err != nil {
		return nil, epubFailure(domain.FailureMalformedDocument, err)
	}
	if int64(len(contents)) > maximumEntry || *expanded > maximumExpanded-int64(len(contents)) {
		return nil, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB expansion limit exceeded"))
	}
	*expanded += int64(len(contents))
	return contents, nil
}

func decodeStrictXML(contents []byte, target any) error {
	if len(contents) == 0 || bytes.Contains(bytes.ToLower(contents), []byte("<!doctype")) ||
		bytes.Contains(bytes.ToLower(contents), []byte("<!entity")) {
		return errors.New("unsafe XML")
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	decoder.Strict = true
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.InputOffset() != int64(len(contents)) {
		return errors.New("trailing XML")
	}
	return nil
}

func extractEPUBXHTML(contents []byte, maximum int64) (string, error) {
	if maximum < 1 || len(contents) == 0 || bytes.Contains(bytes.ToLower(contents), []byte("<!doctype")) ||
		bytes.Contains(bytes.ToLower(contents), []byte("<!entity")) {
		return "", epubFailure(domain.FailureMalformedDocument, errors.New("unsafe EPUB XHTML"))
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	decoder.Strict = true
	var output strings.Builder
	var depth, skipDepth, tokens int
	var sawHTML bool
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", epubFailure(domain.FailureMalformedDocument, err)
		}
		tokens++
		if tokens > maximumXMLTokens {
			return "", epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB XML token limit exceeded"))
		}
		switch value := token.(type) {
		case xml.Directive:
			return "", epubFailure(domain.FailureMalformedDocument, errors.New("EPUB XML directive rejected"))
		case xml.StartElement:
			depth++
			if depth > maximumXMLDepth || len(value.Attr) > maximumXMLAttributes {
				return "", epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB XML structure limit exceeded"))
			}
			name := strings.ToLower(value.Name.Local)
			if name == "html" {
				sawHTML = true
			}
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if name == "script" || name == "style" || name == "svg" || name == "math" {
				skipDepth = 1
				continue
			}
			if epubBlockElement(name) {
				appendEPUBText(&output, "\n", maximum)
			}
		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
			} else if epubBlockElement(strings.ToLower(value.Name.Local)) {
				appendEPUBText(&output, "\n", maximum)
			}
			depth--
			if depth < 0 {
				return "", epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB XML nesting"))
			}
		case xml.CharData:
			if skipDepth == 0 {
				appendEPUBText(&output, string(value), maximum)
			}
		}
		if int64(output.Len()) > maximum {
			return "", epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB text limit exceeded"))
		}
	}
	if !sawHTML || depth != 0 {
		return "", epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB XHTML document"))
	}
	text := normalizeEPUBText(output.String())
	if int64(len(text)) > maximum {
		return "", epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB text limit exceeded"))
	}
	return text, nil
}

func appendEPUBText(output *strings.Builder, value string, maximum int64) {
	if int64(output.Len()) > maximum {
		return
	}
	remaining := maximum - int64(output.Len()) + 1
	if int64(len(value)) > remaining {
		value = value[:remaining]
	}
	_, _ = output.WriteString(value)
}

func normalizeEPUBText(value string) string {
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

func epubBlockElement(name string) bool {
	switch name {
	case "address", "article", "aside", "blockquote", "br", "dd", "div", "dl", "dt", "figcaption",
		"figure", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main",
		"nav", "ol", "p", "pre", "section", "table", "tbody", "td", "th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

func epubFailure(category domain.FailureCategory, cause error) error {
	return &categorizedError{category: category, cause: cause}
}

// EPUBParserExitCode converts a categorized parser failure to the stable,
// diagnostics-free process protocol consumed by the parent EPUB adapter.
func EPUBParserExitCode(err error) int {
	category, categorized := FailureCategory(err)
	if !categorized {
		return EPUBParserExitInternal
	}
	switch category {
	case domain.FailureMalformedDocument:
		return EPUBParserExitMalformed
	case domain.FailureResourceLimitExceeded:
		return EPUBParserExitResourceLimit
	default:
		return EPUBParserExitInternal
	}
}

type epubOutputHeader struct {
	SchemaVersion string `json:"schema_version"`
	LocationCount uint32 `json:"location_count"`
}

type epubOutputLocation struct {
	Location uint32 `json:"location"`
	Text     string `json:"text"`
}

// WriteEPUBOutput is used only by the sandboxed parser executable.
func WriteEPUBOutput(writer io.Writer, pages []Page) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(epubOutputHeader{SchemaVersion: epubOutputSchemaVersion, LocationCount: uint32(len(pages))}); err != nil { // #nosec G115 -- bounded by EPUB limits.
		return err
	}
	for _, page := range pages {
		if err := encoder.Encode(epubOutputLocation{Location: page.Number, Text: page.Text}); err != nil {
			return err
		}
	}
	return nil
}

// EPUB executes the EPUB parser through the same fail-closed sandbox as
// Poppler, then validates its bounded output before exposing spine locations.
type EPUB struct {
	parserPath string
	limits     Limits
	runner     Runner
}

func NewEPUB(parserPath string, limits Limits, runner Runner) *EPUB {
	if runner == nil {
		runner = SandboxedExecRunner{delegate: ExecRunner{}}
	}
	return &EPUB{parserPath: parserPath, limits: limits, runner: runner}
}

func (e *EPUB) Extract(ctx context.Context, sourcePath string, consume func(Page) error) (DocumentInfo, error) {
	if e.parserPath == "" || consume == nil || e.limits.MaximumPages == 0 || e.limits.MaximumPageBytes < 1 ||
		e.limits.MaximumExtractedBytes < 1 {
		return DocumentInfo{}, epubFailure(domain.FailureInternalProcessing, errors.New("invalid EPUB extractor configuration"))
	}
	maximumOutput := e.limits.MaximumExtractedBytes + int64(e.limits.MaximumPages)*128 + 1024
	output, err := e.runner.Run(ctx, e.parserPath, []string{sourcePath}, maximumOutput)
	if err != nil {
		return DocumentInfo{}, classifyEPUBCommandError(ctx, err)
	}
	reader := bufio.NewReaderSize(bytes.NewReader(output), 64<<10)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var header epubOutputHeader
	if err = decoder.Decode(&header); err != nil || header.SchemaVersion != epubOutputSchemaVersion ||
		header.LocationCount == 0 || header.LocationCount > e.limits.MaximumPages {
		return DocumentInfo{}, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB parser output"))
	}
	var extracted int64
	for location := uint32(1); location <= header.LocationCount; location++ {
		var value epubOutputLocation
		if err = decoder.Decode(&value); err != nil || value.Location != location || int64(len(value.Text)) > e.limits.MaximumPageBytes {
			return DocumentInfo{}, epubFailure(domain.FailureMalformedDocument, errors.New("invalid EPUB parser location"))
		}
		extracted += int64(len(value.Text))
		if extracted > e.limits.MaximumExtractedBytes {
			return DocumentInfo{}, epubFailure(domain.FailureResourceLimitExceeded, errors.New("EPUB extracted text limit exceeded"))
		}
		if err = consume(Page{Number: value.Location, Text: value.Text}); err != nil {
			return DocumentInfo{}, err
		}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return DocumentInfo{}, epubFailure(domain.FailureMalformedDocument, errors.New("trailing EPUB parser output"))
	}
	return DocumentInfo{PageCount: header.LocationCount}, nil
}

func classifyEPUBCommandError(ctx context.Context, err error) error {
	if category, ok := FailureCategory(err); ok {
		return epubFailure(category, err)
	}
	if ctx.Err() == nil && !errors.Is(err, exec.ErrNotFound) {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case EPUBParserExitMalformed:
				return epubFailure(domain.FailureMalformedDocument, err)
			case EPUBParserExitResourceLimit:
				return epubFailure(domain.FailureResourceLimitExceeded, err)
			case EPUBParserExitInternal:
				return epubFailure(domain.FailureInternalProcessing, err)
			}
		}
	}
	classified := classifyCommandError(ctx, err)
	if category, ok := FailureCategory(classified); ok {
		return epubFailure(category, err)
	}
	return epubFailure(domain.FailureInternalProcessing, err)
}

func (e *EPUB) String() string {
	return fmt.Sprintf("EPUB{parser:%q}", e.parserPath)
}
