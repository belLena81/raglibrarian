package domain

import "testing"

func TestEvidenceProfilesSharePhysicalCollectionSchema(t *testing.T) {
	pdf, ok := SupportedIndexProfileForMediaType(MediaTypePDF)
	if !ok {
		t.Fatal("PDF profile is not registered")
	}
	epub, ok := SupportedIndexProfileForMediaType(MediaTypeEPUB)
	if !ok {
		t.Fatal("EPUB profile is not registered")
	}
	if pdf.Digest == epub.Digest {
		t.Fatal("format-specific evidence profiles have the same digest")
	}
	if pdf.ExtractionVersion != "poppler-layout-v1" || epub.ExtractionVersion != "epub-spine-v1" {
		t.Fatalf("unexpected extraction profiles: PDF=%q EPUB=%q", pdf.ExtractionVersion, epub.ExtractionVersion)
	}
	if pdf.Model != epub.Model || pdf.Revision != epub.Revision || pdf.Dimensions != epub.Dimensions ||
		pdf.Distance != epub.Distance || pdf.IndexSchema != epub.IndexSchema {
		t.Fatal("profiles cannot safely share the physical collection")
	}
	if CollectionSchemaDigest() == ([32]byte{}) {
		t.Fatal("collection schema digest is empty")
	}
}

func TestEvidenceProfileRegistryRejectsUntrustedMediaType(t *testing.T) {
	if _, ok := SupportedIndexProfileForMediaType("text/html"); ok {
		t.Fatal("unsupported media type was accepted")
	}
}
