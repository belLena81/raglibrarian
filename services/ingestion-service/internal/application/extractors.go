package application

import (
	"errors"
	"strings"
)

const (
	MediaTypePDF  = "application/pdf"
	MediaTypeEPUB = "application/epub+zip"
)

type ExtractionAdapter struct {
	MediaType string
	Extension string
	Version   string
	Extractor Extractor
}

type ExtractorSelector interface {
	Select(string) (ExtractionAdapter, error)
}

type FormatExtractors struct {
	formats map[string]ExtractionAdapter
}

func NewFormatExtractors(adapters ...ExtractionAdapter) (*FormatExtractors, error) {
	if len(adapters) == 0 {
		return nil, errors.New("at least one extraction adapter is required")
	}
	formats := make(map[string]ExtractionAdapter, len(adapters))
	for _, adapter := range adapters {
		if adapter.MediaType == "" || adapter.Extension == "" || !strings.HasPrefix(adapter.Extension, ".") ||
			adapter.Version == "" || adapter.Extractor == nil {
			return nil, errors.New("invalid extraction adapter")
		}
		if _, duplicate := formats[adapter.MediaType]; duplicate {
			return nil, errors.New("duplicate extraction media type")
		}
		formats[adapter.MediaType] = adapter
	}
	return &FormatExtractors{formats: formats}, nil
}

func (s *FormatExtractors) Select(mediaType string) (ExtractionAdapter, error) {
	adapter, found := s.formats[mediaType]
	if !found {
		return ExtractionAdapter{}, ErrUnsupportedProcessingProfile
	}
	return adapter, nil
}
