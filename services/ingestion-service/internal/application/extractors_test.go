package application

import (
	"context"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

type formatExtractorStub struct{ name string }

func (*formatExtractorStub) Extract(context.Context, string, func(extractor.Page) error) (extractor.DocumentInfo, error) {
	return extractor.DocumentInfo{}, nil
}

func TestFormatExtractorsSelectsOnlyExactTrustedMediaType(t *testing.T) {
	pdf := &formatExtractorStub{name: "pdf"}
	epub := &formatExtractorStub{name: "epub"}
	selector, err := NewFormatExtractors(
		ExtractionAdapter{MediaType: MediaTypePDF, Extension: ".pdf", Version: extractor.ExtractionVersion, Extractor: pdf},
		ExtractionAdapter{MediaType: MediaTypeEPUB, Extension: ".epub", Version: extractor.EPUBExtractionVersion, Extractor: epub},
	)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selector.Select(MediaTypeEPUB)
	if err != nil || selected.Extractor != epub || selected.Extension != ".epub" || selected.Version != extractor.EPUBExtractionVersion {
		t.Fatalf("EPUB selection = (%+v, %v)", selected, err)
	}
	for _, value := range []string{"Application/EPUB+ZIP", "application/zip", "application/epub+zip; charset=utf-8", ""} {
		if _, err = selector.Select(value); err != ErrUnsupportedProcessingProfile {
			t.Fatalf("Select(%q) error = %v", value, err)
		}
	}
}
