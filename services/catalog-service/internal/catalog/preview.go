package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	htmlpkg "html"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

var previewTempDir = os.MkdirTemp
var previewExecCommand = runCommand

func defaultPreviewBook(ctx context.Context, book Book, objects OriginalObjectStore, maximumPreviewBytes, maximumPreviewPages, maximumPreviewEPUBEntries int) (string, error) {
	if objects == nil || book.ObjectReference == "" {
		return "", errors.New("preview unavailable")
	}
	source, err := objects.Get(ctx, book.ObjectReference)
	if err != nil {
		return "", err
	}
	defer source.Close()

	suffix := ".pdf"
	if book.MediaType == "application/epub+zip" {
		suffix = ".epub"
	}
	workDir, err := previewTempDir("", "catalog-preview-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	inputDir := filepath.Join(workDir, "input")
	if err = os.MkdirAll(inputDir, 0o700); err != nil {
		return "", err
	}
	outputDir := filepath.Join(workDir, "output")
	if err = os.MkdirAll(outputDir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.Create(filepath.Join(inputDir, "source"+suffix)) // #nosec G304 -- path is created inside an exclusive temp directory.
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(tmp, source); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}

	switch book.MediaType {
	case "application/pdf":
		return previewPDFFragment(ctx, tmp.Name(), outputDir, maximumPreviewPages)
	case "application/epub+zip":
		return previewEPUBFragment(tmp.Name(), maximumPreviewBytes, maximumPreviewPages, maximumPreviewEPUBEntries)
	default:
		return "", fmt.Errorf("unsupported media type %q", book.MediaType)
	}
}

func previewPDFFragment(ctx context.Context, sourcePath, outputDir string, maximumPreviewPages int) (string, error) {
	if maximumPreviewPages <= 0 {
		maximumPreviewPages = defaultMaxPreviewPages
	}
	outputPattern := filepath.Join(outputDir, "catalog-preview-page-%03d.pdf")
	if err := previewSandboxCommand(ctx, "/usr/bin/pdfseparate", "-f", "1", "-l", strconv.Itoa(maximumPreviewPages), sourcePath, outputPattern); err != nil {
		return "", err
	}

	pagePaths := make([]string, 0, maximumPreviewPages)
	for page := 1; page <= maximumPreviewPages; page++ {
		pagePath := fmt.Sprintf(outputPattern, page)
		if _, err := os.Stat(pagePath); err == nil {
			pagePaths = append(pagePaths, pagePath)
		}
	}
	if len(pagePaths) == 0 {
		return "", errors.New("empty PDF preview")
	}

	fragmentPath := filepath.Join(outputDir, "catalog-preview-fragment.pdf")
	if err := previewSandboxCommand(ctx, "/usr/bin/pdfunite", append(pagePaths, fragmentPath)...); err != nil {
		return "", err
	}
	data, err := os.ReadFile(fragmentPath) // #nosec G304 -- path is derived from exclusive temp-directory output.
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("empty PDF preview")
	}
	return "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data), nil
}

func previewEPUBFragment(sourcePath string, maximumPreviewBytes, maximumPreviewPages, maximumPreviewEPUBEntries int) (string, error) {
	pages, err := extractEPUBPreviewPages(sourcePath, maximumPreviewBytes, maximumPreviewPages, maximumPreviewEPUBEntries)
	if err != nil {
		return "", err
	}
	if len(pages) == 0 {
		return "", errors.New("empty EPUB preview")
	}
	var html strings.Builder
	html.Grow(1024)
	html.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><style>")
	html.WriteString("body{margin:0;padding:16px;background:#f8fafc;color:#0f172a;font:14px/1.5 system-ui,sans-serif}")
	html.WriteString(".page{margin:0 0 16px;border:1px solid #cbd5e1;border-radius:12px;overflow:hidden;background:#fff;box-shadow:0 1px 2px rgba(15,23,42,.08)}")
	html.WriteString(".page iframe{width:100%;height:70vh;border:0;display:block;background:#fff}")
	html.WriteString("</style></head><body>")
	for index, page := range pages {
		html.WriteString("<section class=\"page\"><iframe sandbox=\"\" title=\"Page ")
		html.WriteString(strconv.Itoa(index + 1))
		html.WriteString("\" srcdoc=\"")
		html.WriteString(htmlpkg.EscapeString(injectEPUBPreviewCSP(page)))
		html.WriteString("\"></iframe></section>")
	}
	html.WriteString("</body></html>")
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html.String())), nil
}

const epubPreviewCSP = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; media-src 'none'; object-src 'none'; connect-src 'none'; frame-src 'none'; child-src 'none'">`

func injectEPUBPreviewCSP(page []byte) string {
	content := string(page)
	if insertAt := epubPreviewHeadInsertOffset(content); insertAt >= 0 {
		return content[:insertAt] + epubPreviewCSP + content[insertAt:]
	}
	return "<!doctype html><html><head><meta charset=\"utf-8\">" + epubPreviewCSP + "</head><body>" + content + "</body></html>"
}

func epubPreviewHeadInsertOffset(content string) int {
	decoder := xml.NewDecoder(strings.NewReader(content))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return -1
		}
		if err != nil {
			return -1
		}
		if start, ok := token.(xml.StartElement); ok && strings.EqualFold(start.Name.Local, "head") {
			return int(decoder.InputOffset())
		}
	}
}

func extractEPUBPreviewPages(sourcePath string, maximumPreviewBytes, maximumPreviewPages, maximumPreviewEPUBEntries int) ([][]byte, error) {
	if maximumPreviewPages <= 0 {
		maximumPreviewPages = defaultMaxPreviewPages
	}
	if maximumPreviewEPUBEntries <= 0 {
		maximumPreviewEPUBEntries = defaultMaxEPUBEntries
	}
	archive, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.Close() }()
	if len(archive.File) < 3 {
		return nil, errors.New("invalid EPUB archive")
	}
	if len(archive.File) > maximumPreviewEPUBEntries {
		return nil, errors.New("invalid EPUB archive")
	}

	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if !validEPUBArchivePath(file.Name) {
			return nil, errors.New("invalid EPUB archive")
		}
		files[file.Name] = file
	}

	mimetype, err := readEPUBEntry(files["mimetype"], maximumPreviewBytes)
	if err != nil {
		return nil, err
	}
	if string(mimetype) != "application/epub+zip" {
		return nil, errors.New("invalid EPUB archive")
	}
	containerBytes, err := readEPUBEntry(files["META-INF/container.xml"], maximumPreviewBytes)
	if err != nil {
		return nil, err
	}
	var container epubContainer
	if err = xml.Unmarshal(containerBytes, &container); err != nil || len(container.Rootfiles) != 1 {
		return nil, errors.New("invalid EPUB container")
	}
	rootfile := container.Rootfiles[0]
	if rootfile.Media != "application/oebps-package+xml" || !validEPUBArchivePath(rootfile.FullPath) {
		return nil, errors.New("invalid EPUB package reference")
	}
	packageBytes, err := readEPUBEntry(files[rootfile.FullPath], maximumPreviewBytes)
	if err != nil {
		return nil, err
	}
	var publication epubPackage
	if err = xml.Unmarshal(packageBytes, &publication); err != nil || len(publication.Manifest) == 0 || len(publication.Spine) == 0 {
		return nil, errors.New("invalid EPUB package")
	}

	manifest := make(map[string]string, len(publication.Manifest))
	packageDirectory := path.Dir(rootfile.FullPath)
	for _, item := range publication.Manifest {
		if !validEPUBIdentifier(item.ID) || item.Media != "application/xhtml+xml" {
			continue
		}
		reference, resolveErr := resolveEPUBReference(packageDirectory, item.Href)
		if resolveErr != nil {
			return nil, resolveErr
		}
		manifest[item.ID] = reference
	}

	pages := make([][]byte, 0, maximumPreviewPages)
	seen := make(map[string]struct{}, len(publication.Spine))
	for _, item := range publication.Spine {
		reference, found := manifest[item.IDRef]
		if !found {
			return nil, errors.New("invalid EPUB spine reference")
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, errors.New("duplicate EPUB spine reference")
		}
		seen[reference] = struct{}{}
		pageBytes, pageErr := readEPUBEntry(files[reference], maximumPreviewBytes)
		if pageErr != nil {
			return nil, pageErr
		}
		pages = append(pages, pageBytes)
		if len(pages) == maximumPreviewPages {
			break
		}
	}
	return pages, nil
}

func previewSandboxCommand(ctx context.Context, command string, args ...string) error {
	return previewExecCommand(ctx, previewSandboxPath(), append([]string{command}, args...)...)
}

func previewSandboxPath() string {
	if value := strings.TrimSpace(os.Getenv("PARSER_SANDBOX_PATH")); value != "" {
		return value
	}
	return "/parser-sandbox"
}

func runCommand(ctx context.Context, path string, args ...string) error {
	command := exec.CommandContext(ctx, path, args...) // #nosec G204,G702 -- sandbox path comes from trusted local configuration for the preview wrapper.
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return err
	}
	return nil
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

func readEPUBEntry(file *zip.File, maximumPreviewBytes int) ([]byte, error) {
	if file == nil {
		return nil, errors.New("invalid EPUB archive")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	if maximumPreviewBytes <= 0 {
		maximumPreviewBytes = defaultMaxPreviewBytes
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(maximumPreviewBytes)))
	if err != nil {
		return nil, err
	}
	return data, nil
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
	fragment := strings.IndexByte(href, '#')
	if fragment == 0 {
		return "", errors.New("invalid EPUB manifest reference")
	}
	if fragment > 0 {
		href = href[:fragment]
	}
	if href == "" || strings.Contains(href, `\`) || strings.HasPrefix(href, "/") || strings.ContainsRune(href, 0) {
		return "", errors.New("invalid EPUB manifest reference")
	}
	referenceParts := make([]string, 0, 4)
	for _, segment := range strings.Split(href, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("invalid EPUB manifest reference")
		}
		decoded, err := urlPathUnescape(segment)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, `/\`) ||
			strings.ContainsRune(decoded, 0) {
			return "", errors.New("invalid EPUB manifest reference")
		}
		referenceParts = append(referenceParts, decoded)
	}
	reference := path.Clean(path.Join(append([]string{directory}, referenceParts...)...))
	if !validEPUBArchivePath(reference) {
		return "", errors.New("invalid EPUB manifest reference")
	}
	return reference, nil
}

func urlPathUnescape(value string) (string, error) {
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", err
	}
	return decoded, nil
}
