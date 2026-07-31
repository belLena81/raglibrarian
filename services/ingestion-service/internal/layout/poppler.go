package layout

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

const layoutSchemaVersion = "v1"

const popplerXHTMLDoctype = `DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd"`
const popplerXHTMLNamespace = "http://www.w3.org/1999/xhtml"

type AnalyzerConfig struct {
	PDFTextPath             string
	EPUBParserPath          string
	MaximumPages            uint32
	MaximumItemsPerLocation int
	MaximumXMLTokens        int
	MaximumXMLDepth         int
	MaximumOutputBytes      int64
	MaximumPageTextBytes    int64
	MaximumItemTextBytes    int64
	MaximumTextBytes        int64
	EPUBArchiveLimits       extractor.EPUBArchiveLimits
}

type PopplerAnalyzer struct {
	config AnalyzerConfig
	runner extractor.Runner
}

func NewPopplerAnalyzer(config AnalyzerConfig, runner extractor.Runner) (*PopplerAnalyzer, error) {
	if strings.TrimSpace(config.PDFTextPath) == "" || config.MaximumPages == 0 ||
		config.MaximumItemsPerLocation < 1 || config.MaximumXMLTokens < 1 || config.MaximumXMLDepth < 1 ||
		config.MaximumOutputBytes < 1 || config.MaximumPageTextBytes < 1 || config.MaximumItemTextBytes < 1 ||
		config.MaximumTextBytes < 1 || config.MaximumPageTextBytes > config.MaximumTextBytes ||
		config.MaximumItemTextBytes > config.MaximumPageTextBytes ||
		!validExecutablePath(config.PDFTextPath) || (config.EPUBParserPath != "" && !validExecutablePath(config.EPUBParserPath)) {
		return nil, errors.New("invalid layout analyzer configuration")
	}
	if runner == nil {
		runner = extractor.SandboxedExecRunner{}
	}
	return &PopplerAnalyzer{config: config, runner: runner}, nil
}

func validExecutablePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && strings.TrimSpace(value) == value
}

func (analyzer *PopplerAnalyzer) Analyze(ctx context.Context, sourcePath, mediaType string) (Document, error) {
	switch mediaType {
	case "application/pdf":
		return analyzer.analyzePDF(ctx, sourcePath)
	case "application/epub+zip":
		return analyzer.analyzeEPUB(ctx, sourcePath)
	default:
		return Document{}, errors.New("unsupported layout media type")
	}
}

func (analyzer *PopplerAnalyzer) analyzePDF(ctx context.Context, sourcePath string) (Document, error) {
	output, err := analyzer.runner.Run(ctx, analyzer.config.PDFTextPath,
		[]string{"-bbox-layout", "-enc", "UTF-8", sourcePath, "-"}, analyzer.config.MaximumOutputBytes+1)
	if err != nil {
		return Document{}, errors.New("layout extraction failed")
	}
	if int64(len(output)) > analyzer.config.MaximumOutputBytes {
		return Document{}, errors.New("layout output limit exceeded")
	}
	return analyzer.parseBBoxLayout(output)
}

func (analyzer *PopplerAnalyzer) analyzeEPUB(ctx context.Context, sourcePath string) (Document, error) {
	if strings.TrimSpace(analyzer.config.EPUBParserPath) == "" {
		return Document{}, errors.New("EPUB layout parser unavailable")
	}
	parser := extractor.NewEPUB(analyzer.config.EPUBParserPath, extractor.Limits{
		MaximumPages: analyzer.config.MaximumPages, MaximumPageBytes: analyzer.config.MaximumPageTextBytes,
		MaximumExtractedBytes: analyzer.config.MaximumTextBytes,
	}, analyzer.config.EPUBArchiveLimits, analyzer.runner)
	document := Document{SchemaVersion: layoutSchemaVersion}
	var totalText int64
	_, err := parser.Extract(ctx, sourcePath, func(page extractor.Page) error {
		location := Location{Ordinal: page.Number}
		for _, paragraph := range splitEPUBText(page.Text) {
			if len(location.Items) >= analyzer.config.MaximumItemsPerLocation {
				return errors.New("layout item limit exceeded")
			}
			totalText += int64(len(paragraph))
			if int64(len(paragraph)) > analyzer.config.MaximumItemTextBytes || totalText > analyzer.config.MaximumTextBytes {
				return errors.New("layout text limit exceeded")
			}
			location.Items = append(location.Items, newLayoutItem(classifyText(paragraph, len(location.Items)), "body", paragraph, Bounds{}))
		}
		document.Locations = append(document.Locations, location)
		return nil
	})
	if err != nil || totalText == 0 {
		return Document{}, errors.New("no trusted layout text")
	}
	return document, nil
}

func splitEPUBText(value string) []string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if text := strings.Join(strings.Fields(line), " "); text != "" {
			result = append(result, text)
		}
	}
	return result
}

type bboxPage struct {
	width, height float64
	textBytes     int64
	location      Location
}

func (analyzer *PopplerAnalyzer) parseBBoxLayout(output []byte) (Document, error) {
	decoder := xml.NewDecoder(bytes.NewReader(output))
	decoder.Strict = true
	document := Document{SchemaVersion: layoutSchemaVersion}
	var page *bboxPage
	var line *Item
	var word strings.Builder
	var textBytes int64
	var metadataTextBytes int64
	var stack []string
	tokenCount := 0
	docSeen := false
	docClosed := false
	rootSeen := false
	rootClosed := false
	directiveSeen := false
	processingInstructionSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Document{}, errors.New("invalid layout output")
		}
		tokenCount++
		if tokenCount > analyzer.config.MaximumXMLTokens {
			return Document{}, errors.New("layout token limit exceeded")
		}
		switch value := token.(type) {
		case xml.Directive:
			if directiveSeen || rootSeen || strings.TrimSpace(string(value)) != popplerXHTMLDoctype {
				return Document{}, errors.New("unsafe layout output")
			}
			directiveSeen = true
		case xml.ProcInst:
			if processingInstructionSeen || rootSeen || value.Target != "xml" {
				return Document{}, errors.New("unsafe layout output")
			}
			processingInstructionSeen = true
		case xml.Comment:
			return Document{}, errors.New("unsafe layout output")
		case xml.CharData:
			if len(stack) > 0 && stack[len(stack)-1] == "word" {
				if int64(word.Len()+len(value)) > analyzer.config.MaximumItemTextBytes {
					return Document{}, errors.New("layout item text limit exceeded")
				}
				_, _ = word.Write(value)
			} else if len(stack) > 0 && stack[len(stack)-1] == "title" {
				metadataTextBytes += int64(len(value))
				if metadataTextBytes > analyzer.config.MaximumItemTextBytes {
					return Document{}, errors.New("layout metadata text limit exceeded")
				}
			} else if strings.TrimSpace(string(value)) != "" {
				return Document{}, errors.New("invalid layout output")
			}
		case xml.StartElement:
			name := value.Name.Local
			if value.Name.Space != "" && value.Name.Space != popplerXHTMLNamespace {
				return Document{}, errors.New("invalid layout namespace")
			}
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			if !validBBoxElement(name, parent) || rootClosed || (parent == "" && rootSeen) {
				return Document{}, errors.New("invalid layout output")
			}
			stack = append(stack, name)
			if len(stack) > analyzer.config.MaximumXMLDepth {
				return Document{}, errors.New("layout depth limit exceeded")
			}
			if len(stack) == 1 {
				rootSeen = true
			}
			switch value.Name.Local {
			case "doc":
				if docSeen {
					return Document{}, errors.New("invalid layout output")
				}
				docSeen = true
			case "page":
				if page != nil || len(document.Locations) >= int(analyzer.config.MaximumPages) {
					return Document{}, errors.New("layout page limit exceeded")
				}
				width, height, parseErr := pageDimensions(value.Attr)
				if parseErr != nil {
					return Document{}, parseErr
				}
				page = &bboxPage{width: width, height: height, location: Location{Ordinal: uint32(len(document.Locations) + 1)}} // #nosec G115 -- length is bounded by the configured uint32 page maximum above.
			case "line":
				if page == nil || line != nil || len(page.location.Items) >= analyzer.config.MaximumItemsPerLocation {
					return Document{}, errors.New("layout item limit exceeded")
				}
				bounds, parseErr := elementBounds(value.Attr)
				if parseErr != nil {
					return Document{}, parseErr
				}
				if bounds.Left < 0 || bounds.Top < 0 || bounds.Right > page.width+1 || bounds.Bottom > page.height+1 {
					return Document{}, errors.New("item bounds exceed page")
				}
				item := newLayoutItem("paragraph", "body", "", bounds)
				line = &item
			case "word":
				if line == nil {
					return Document{}, errors.New("invalid layout output")
				}
				word.Reset()
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != value.Name.Local {
				return Document{}, errors.New("invalid layout output")
			}
			switch value.Name.Local {
			case "word":
				value := strings.Join(strings.Fields(word.String()), " ")
				if value != "" {
					separatorBytes := 0
					if line.Text != "" {
						separatorBytes = 1
					}
					if int64(len(line.Text)+separatorBytes+len(value)) > analyzer.config.MaximumItemTextBytes {
						return Document{}, errors.New("layout item text limit exceeded")
					}
					if separatorBytes == 1 {
						line.Text += " "
					}
					line.Text += value
				}
				word.Reset()
			case "line":
				if page == nil || line == nil {
					return Document{}, errors.New("invalid layout output")
				}
				line.Text = strings.TrimSpace(line.Text)
				if line.Text != "" {
					if !utf8.ValidString(line.Text) {
						return Document{}, errors.New("invalid layout text")
					}
					textBytes += int64(len(line.Text))
					page.textBytes += int64(len(line.Text))
					if textBytes > analyzer.config.MaximumTextBytes || page.textBytes > analyzer.config.MaximumPageTextBytes {
						return Document{}, errors.New("layout text limit exceeded")
					}
					line.Label, line.ContentLayer = classifyPDFLine(*line, *page, len(page.location.Items))
					page.location.Items = append(page.location.Items, *line)
				}
				line = nil
			case "page":
				if page == nil || line != nil {
					return Document{}, errors.New("invalid layout output")
				}
				document.Locations = append(document.Locations, page.location)
				page = nil
			case "doc":
				docClosed = true
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				rootClosed = true
			}
		}
	}
	if !rootSeen || !rootClosed || !docSeen || !docClosed || len(stack) != 0 || page != nil || line != nil || textBytes == 0 || len(document.Locations) == 0 {
		return Document{}, errors.New("no trusted layout text")
	}
	return document, nil
}

func validBBoxElement(name, parent string) bool {
	switch name {
	case "html":
		return parent == ""
	case "head", "body":
		return parent == "html"
	case "meta", "title":
		return parent == "head"
	case "doc":
		return parent == "" || parent == "body"
	case "page":
		return parent == "doc"
	case "flow":
		return parent == "page"
	case "block":
		return parent == "flow"
	case "line":
		return parent == "block"
	case "word":
		return parent == "line"
	default:
		return false
	}
}

func pageDimensions(attributes []xml.Attr) (float64, float64, error) {
	width, widthOK := attributeFloat(attributes, "width")
	height, heightOK := attributeFloat(attributes, "height")
	if !widthOK || !heightOK || width <= 0 || height <= 0 || !finiteCoordinate(width) || !finiteCoordinate(height) {
		return 0, 0, errors.New("invalid page bounds")
	}
	return width, height, nil
}

func elementBounds(attributes []xml.Attr) (Bounds, error) {
	left, leftOK := attributeFloat(attributes, "xMin")
	top, topOK := attributeFloat(attributes, "yMin")
	right, rightOK := attributeFloat(attributes, "xMax")
	bottom, bottomOK := attributeFloat(attributes, "yMax")
	if !leftOK || !topOK || !rightOK || !bottomOK || !finiteCoordinate(left) || !finiteCoordinate(top) ||
		!finiteCoordinate(right) || !finiteCoordinate(bottom) || right < left || bottom < top {
		return Bounds{}, errors.New("invalid item bounds")
	}
	return Bounds{Left: left, Top: top, Right: right, Bottom: bottom}, nil
}

func attributeFloat(attributes []xml.Attr, name string) (float64, bool) {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			value, err := strconv.ParseFloat(attribute.Value, 64)
			return value, err == nil
		}
	}
	return 0, false
}

func finiteCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -1_000_000 && value <= 1_000_000
}

func classifyPDFLine(item Item, page bboxPage, index int) (string, string) {
	if item.BBox.Top <= page.height*0.06 {
		return "page_header", "furniture"
	}
	if item.BBox.Bottom >= page.height*0.94 {
		return "page_footer", "furniture"
	}
	if index == 0 {
		words := strings.Fields(item.Text)
		if page.location.Ordinal <= 2 && len(words) <= 14 && !looksLikeProtectedHeading(item.Text) {
			return "title", "body"
		}
		if looksLikeSectionHeader(item.Text) {
			return "section_header", "body"
		}
	}
	return classifyText(item.Text, -1), "body"
}

func classifyText(text string, index int) string {
	words := strings.Fields(text)
	trimmed := strings.TrimSpace(text)
	if index == 0 && len(words) <= 14 {
		return "section_header"
	}
	if len(words) <= 24 && (strings.Contains(trimmed, "....") || endsWithPageNumber(trimmed)) {
		return "document_index"
	}
	return "paragraph"
}

func looksLikeProtectedHeading(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"chapter", "part", "prologue", "epilogue", "introduction", "preface", "foreword", "abstract"} {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			return true
		}
	}
	return false
}

func looksLikeSectionHeader(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if looksLikeProtectedHeading(trimmed) {
		return true
	}
	for _, heading := range []string{"contents", "table of contents", "list of figures", "list of tables", "list of illustrations", "index", "general index", "colophon", "other books"} {
		if lower == heading {
			return true
		}
	}
	return len([]rune(trimmed)) > 1 && len(strings.Fields(trimmed)) <= 14 && strings.ToUpper(trimmed) == trimmed
}

func endsWithPageNumber(value string) bool {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return false
	}
	_, err := strconv.ParseUint(strings.Trim(fields[len(fields)-1], "."), 10, 32)
	return err == nil
}

func newLayoutItem(label, layer, text string, bounds Bounds) Item {
	item := Item{Label: label, ContentLayer: layer, Text: text}
	item.BBox.Left = bounds.Left
	item.BBox.Top = bounds.Top
	item.BBox.Right = bounds.Right
	item.BBox.Bottom = bounds.Bottom
	return item
}
