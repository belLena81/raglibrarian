//go:build ignore

// Command generate writes the deterministic synthetic document corpus used by
// the ingestion black-box tests. It intentionally uses only the standard
// library and contains no copied book content.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/md5" // #nosec G501 -- mandated by the synthetic PDF R2 fixture format.
	"crypto/rc4" // #nosec G503 -- mandated by the synthetic PDF R2 fixture format.
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type page struct {
	text  string
	blank bool
}

func main() {
	out := flag.String("out", "", "directory in which to write document fixtures")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "-out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		fatal(err)
	}

	fixtures := map[string][]byte{
		"minimal.pdf": pdf([]page{{text: "Synthetic chapter one. Deterministic ingestion fixture."}}),
		"canary.pdf": pdf([]page{
			{text: "Chapter Canary. M4_CANARY_7D3B9A11 remains confined to encrypted processing artifacts."},
		}),
		"multipage.pdf": pdf([]page{
			{text: "Chapter One. Synthetic systems begin with explicit boundaries and continue across pages."},
			{text: "This continuation deliberately omits a heading so chapter context must be carried forward. Section One. Page citations remain stable across chunk boundaries."},
			{text: "Chapter Two. Deterministic output makes retries harmless."},
		}),
		"blank_middle_page.pdf": pdf([]page{
			{text: "Before the intentionally blank page."},
			{blank: true},
			{text: "After the intentionally blank page."},
		}),
		"image_only.pdf":         imageOnlyPDF(),
		"encrypted.pdf":          encryptedPDF(nil),
		"encrypted_password.pdf": encryptedPDF([]byte("m4-synthetic-user-password")),
		"malformed.pdf":          []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages"),
		"oversize.pdf":           oversizedPDF(),
		"locations.epub":         epub(),
	}

	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, fixtures[name], 0o640); err != nil {
			fatal(err)
		}
	}
}

func epub() []byte {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)

	mimetypeHeader := &zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	}
	mimetype, err := writer.CreateHeader(mimetypeHeader)
	if err != nil {
		panic(err)
	}
	if _, err = mimetype.Write([]byte("application/epub+zip")); err != nil {
		panic(err)
	}

	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="EPUB/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"EPUB/content.opf": `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" unique-identifier="book-id" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="book-id">urn:uuid:2f10cb1d-77cc-4d85-b10d-1c115b1ab926</dc:identifier>
    <dc:title>M7 synthetic locations</dc:title>
    <dc:language>en</dc:language>
  </metadata>
  <manifest>
    <item id="chapter-one" href="chapter-one.xhtml" media-type="application/xhtml+xml"/>
    <item id="chapter-two" href="chapter-two.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="chapter-one"/>
    <itemref idref="chapter-two"/>
  </spine>
</package>`,
		"EPUB/chapter-one.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" lang="en">
  <head><title>One</title></head>
  <body><h1>Chapter One</h1><p>Synthetic EPUB evidence starts at the first location.</p></body>
</html>`,
		"EPUB/chapter-two.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" lang="en">
  <head><title>Two</title></head>
  <body><h1>Chapter Two</h1><p>Clockwork indexes converge after a replayed command.</p></body>
</html>`,
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file, createErr := writer.Create(name)
		if createErr != nil {
			panic(createErr)
		}
		if _, writeErr := file.Write([]byte(files[name])); writeErr != nil {
			panic(writeErr)
		}
	}
	if err = writer.Close(); err != nil {
		panic(err)
	}
	return output.Bytes()
}

func oversizedPDF() []byte {
	const maximumUploadBytes = 64 << 20
	value := pdf([]page{{text: "Synthetic document whose transport body exceeds the upload limit."}})
	// Bytes after %%EOF are legal PDF trailing data. This keeps the fixture
	// syntactically valid so the test exercises the upload bound, not parsing.
	return append(value, make([]byte, maximumUploadBytes+1-len(value))...)
}

func pdf(pages []page) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	kids := make([]string, 0, len(pages))
	for _, current := range pages {
		pageID := len(objects) + 1
		contentID := pageID + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentID),
			stream(pageContent(current)),
		)
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), join(kids))
	return serialize(objects, false)
}

func pageContent(value page) string {
	if value.blank {
		return ""
	}
	return fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", escapePDFString(value.text))
}

func imageOnlyPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		stream("q 100 0 0 100 72 620 cm /Im0 Do Q\n"),
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /ASCIIHexDecode /Length 3 >>\nstream\n00>\nendstream",
	}
	return serialize(objects, false)
}

func encryptedPDF(userPassword []byte) []byte {
	const contentObjectID = 5
	owner, user, fileKey := legacyPDFEncryption([]byte("synthetic-owner"), userPassword)
	content := []byte("BT /F1 12 Tf 72 720 Td (Synthetic encrypted fixture.) Tj ET\n")
	objectKeyMaterial := append(append([]byte(nil), fileKey...), byte(contentObjectID), 0, 0, 0, 0)
	objectDigest := md5.Sum(objectKeyMaterial)      // #nosec G401 -- mandated by the synthetic PDF R2 fixture format.
	cipher, err := rc4.NewCipher(objectDigest[:10]) // #nosec G503 -- mandated by the synthetic PDF R2 fixture format.
	if err != nil {
		fatal(err)
	}
	encryptedContent := make([]byte, len(content))
	cipher.XORKeyStream(encryptedContent, content)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [4 0 R] >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>",
		stream(string(encryptedContent)),
		fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /Length 40 /O <%s> /U <%s> /P -4 >>", hex.EncodeToString(owner), hex.EncodeToString(user)),
	}
	return serialize(objects, true)
}

func legacyPDFEncryption(ownerPassword, userPassword []byte) ([]byte, []byte, []byte) {
	fileID := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	ownerDigest := md5.Sum(padPDFPassword(ownerPassword)) // #nosec G401 -- mandated by the synthetic PDF R2 fixture format.
	ownerCipher, err := rc4.NewCipher(ownerDigest[:5])    // #nosec G503 -- mandated by the synthetic PDF R2 fixture format.
	if err != nil {
		fatal(err)
	}
	owner := make([]byte, 32)
	ownerCipher.XORKeyStream(owner, padPDFPassword(userPassword))

	material := append(padPDFPassword(userPassword), owner...)
	permissions := int32(-4)
	permissionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(permissionBytes, uint32(permissions))
	material = append(material, permissionBytes...)
	material = append(material, fileID...)
	fileDigest := md5.Sum(material) // #nosec G401 -- mandated by the synthetic PDF R2 fixture format.
	fileKey := append([]byte(nil), fileDigest[:5]...)
	userCipher, err := rc4.NewCipher(fileKey) // #nosec G503 -- mandated by the synthetic PDF R2 fixture format.
	if err != nil {
		fatal(err)
	}
	user := make([]byte, 32)
	userCipher.XORKeyStream(user, pdfPasswordPadding)
	return owner, user, fileKey
}

var pdfPasswordPadding = []byte{
	0x28, 0xbf, 0x4e, 0x5e, 0x4e, 0x75, 0x8a, 0x41,
	0x64, 0x00, 0x4e, 0x56, 0xff, 0xfa, 0x01, 0x08,
	0x2e, 0x2e, 0x00, 0xb6, 0xd0, 0x68, 0x3e, 0x80,
	0x2f, 0x0c, 0xa9, 0xfe, 0x64, 0x53, 0x69, 0x7a,
}

func padPDFPassword(password []byte) []byte {
	if len(password) > 32 {
		password = password[:32]
	}
	padded := append([]byte(nil), password...)
	padded = append(padded, pdfPasswordPadding[:32-len(padded)]...)
	return padded
}

func serialize(objects []string, encrypted bool) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R", len(offsets))
	if encrypted {
		fmt.Fprintf(&out, " /Encrypt %d 0 R /ID [<00112233445566778899aabbccddeeff><00112233445566778899aabbccddeeff>]", len(objects))
	}
	fmt.Fprintf(&out, " >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return out.Bytes()
}

func stream(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)
}

func escapePDFString(value string) string {
	var escaped bytes.Buffer
	for _, char := range value {
		switch char {
		case '\\', '(', ')':
			escaped.WriteByte('\\')
			escaped.WriteRune(char)
		default:
			escaped.WriteRune(char)
		}
	}
	return escaped.String()
}

func join(values []string) string {
	var result bytes.Buffer
	for index, value := range values {
		if index > 0 {
			result.WriteByte(' ')
		}
		result.WriteString(value)
	}
	return result.String()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
