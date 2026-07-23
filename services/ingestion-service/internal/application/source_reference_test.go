package application

import "testing"

func TestValidSourceReferenceMatchesCatalogBase64URLKeys(t *testing.T) {
	for _, reference := range []string{
		"originals/01234567-89ab-cdef-0123-456789abcdef.pdf",
		"originals/Yx_Generated-Base64URL-Key.pdf",
	} {
		if !validSourceReference(reference, MediaTypePDF) {
			t.Fatalf("Catalog-owned reference %q was rejected", reference)
		}
	}
	for _, reference := range []string{
		"../originals/book.pdf",
		"originals/nested/book.pdf",
		"originals/book%2Fother.pdf",
	} {
		if validSourceReference(reference, MediaTypePDF) {
			t.Fatalf("unsafe reference %q was accepted", reference)
		}
	}
	if !validSourceReference("originals/Yx_Generated-Base64URL-Key.epub", MediaTypeEPUB) {
		t.Fatal("valid EPUB reference was rejected")
	}
	if validSourceReference("originals/book.pdf", MediaTypeEPUB) {
		t.Fatal("mismatched EPUB reference was accepted")
	}
}
