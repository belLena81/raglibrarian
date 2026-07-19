//go:build ignore

// Command generate writes the deterministic synthetic PDF corpus used by the
// Milestone 4 black-box tests. It intentionally uses only the standard library
// and contains no copied book content.
package main

import (
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
	out := flag.String("out", "", "directory in which to write PDF fixtures")
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
		"multipage.pdf": pdf([]page{
			{text: "Chapter One. Synthetic systems begin with explicit boundaries."},
			{text: "Section One. Page citations remain stable across chunk boundaries."},
			{text: "Chapter Two. Deterministic output makes retries harmless."},
		}),
		"blank_middle_page.pdf": pdf([]page{
			{text: "Before the intentionally blank page."},
			{blank: true},
			{text: "After the intentionally blank page."},
		}),
		"image_only.pdf": imageOnlyPDF(),
		"encrypted.pdf":  encryptedPDF(),
		"malformed.pdf":  []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages"),
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

func encryptedPDF() []byte {
	const contentObjectID = 5
	owner, user, fileKey := legacyPDFEncryption([]byte("synthetic-owner"), nil)
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
