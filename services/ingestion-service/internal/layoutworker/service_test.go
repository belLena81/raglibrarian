package layoutworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/layout"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/selection"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type testSource struct {
	contents []byte
	size     int64
	err      error
}

func (s testSource) Open(context.Context, string) (io.ReadCloser, int64, error) {
	if s.err != nil {
		return nil, 0, s.err
	}
	return io.NopCloser(bytes.NewReader(s.contents)), s.size, nil
}

type testAnalyzer struct {
	document layout.Document
	err      error
	seen     []byte
}

func (a *testAnalyzer) Analyze(_ context.Context, path, _ string) (layout.Document, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return layout.Document{}, err
	}
	a.seen = contents
	return a.document, a.err
}

func TestServicePublishesOnlyBoundedWholeLocationDecisions(t *testing.T) {
	source := []byte("private uploaded book")
	parserText := "A title that must never enter the event"
	document := layout.Document{SchemaVersion: "v1", Locations: []layout.Location{
		location(1, "title", parserText), location(2, "paragraph", "chapter body"),
		location(3, "paragraph", "chapter body"), location(4, "paragraph", "chapter body"),
		location(5, "paragraph", "chapter body"),
	}}
	analyzer := &testAnalyzer{document: document}
	service := newTestService(t, testSource{contents: source, size: int64(len(source))}, analyzer)
	payload := requestPayload(t, source, ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT)
	eventID, completedPayload, err := service.Process(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(analyzer.seen, source) || strings.Contains(string(completedPayload), parserText) || strings.Contains(string(completedPayload), string(source)) {
		t.Fatal("source-derived content crossed the worker event boundary")
	}
	var completed ingestionv1.BookContentSelectionCompletedV1
	if err = proto.Unmarshal(completedPayload, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.EventId != eventID || completed.RequestId != "request-1" || completed.JobId != "job-1" ||
		completed.OriginalOrdinalCount != 5 || completed.FallbackReason != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE ||
		len(completed.ExcludedRanges) != 1 || completed.ExcludedRanges[0].StartOrdinal != 1 || completed.ExcludedRanges[0].EndOrdinal != 1 ||
		completed.ExcludedRanges[0].Reason != ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE {
		t.Fatalf("unexpected completion: %+v", &completed)
	}
}

func TestServiceParserFailureFailsOpenWithoutContent(t *testing.T) {
	source := []byte("private uploaded book")
	service := newTestService(t, testSource{contents: source, size: int64(len(source))}, &testAnalyzer{err: errors.New("parser details must not escape")})
	_, payload, err := service.Process(context.Background(), requestPayload(t, source, ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT))
	if err != nil {
		t.Fatal(err)
	}
	var completed ingestionv1.BookContentSelectionCompletedV1
	if err = proto.Unmarshal(payload, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.FallbackReason != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT ||
		len(completed.ExcludedRanges) != 0 || completed.OriginalOrdinalCount != 0 || strings.Contains(string(payload), "parser details") {
		t.Fatalf("parser failure did not fail open safely: %+v", &completed)
	}
}

func TestServiceRejectsSourceDigestMismatchBeforeParser(t *testing.T) {
	source := []byte("different bytes")
	analyzer := &testAnalyzer{}
	service := newTestService(t, testSource{contents: source, size: int64(len(source))}, analyzer)
	payload := requestPayload(t, []byte("expected bytes"), ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT)
	var request ingestionv1.BookContentSelectionRequestedV1
	if err := proto.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	request.SourceByteSize = int64(len(source))
	payload, _ = proto.Marshal(&request)
	_, completedPayload, err := service.Process(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var completed ingestionv1.BookContentSelectionCompletedV1
	if err = proto.Unmarshal(completedPayload, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.FallbackReason != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT || len(completed.ExcludedRanges) != 0 {
		t.Fatalf("completion = %+v", &completed)
	}
	if analyzer.seen != nil {
		t.Fatal("parser was called for an untrusted source")
	}
}

func TestServiceSourceUnavailablePublishesFailOpenCompletion(t *testing.T) {
	source := []byte("expected bytes")
	service := newTestService(t, testSource{err: errors.New("private storage detail")}, &testAnalyzer{})
	_, payload, err := service.Process(context.Background(), requestPayload(t, source, ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT))
	if err != nil {
		t.Fatal(err)
	}
	var completed ingestionv1.BookContentSelectionCompletedV1
	if err = proto.Unmarshal(payload, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.FallbackReason != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_RESOURCE_LIMIT ||
		completed.OriginalOrdinalCount != 0 || len(completed.ExcludedRanges) != 0 || strings.Contains(string(payload), "storage detail") {
		t.Fatalf("completion = %+v", &completed)
	}
}

func TestServiceObservationFailureUsesObservationFallback(t *testing.T) {
	source := []byte("expected bytes")
	service := newTestService(t, testSource{err: errors.New("private storage detail")}, &testAnalyzer{})
	_, payload, err := service.Process(context.Background(), requestPayload(t, source, ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION))
	if err != nil {
		t.Fatal(err)
	}
	var completed ingestionv1.BookContentSelectionCompletedV1
	if err = proto.Unmarshal(payload, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.FallbackReason != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION ||
		completed.OriginalOrdinalCount != 0 || len(completed.ExcludedRanges) != 0 {
		t.Fatalf("completion = %+v", &completed)
	}
}

func TestDecodeRequestRejectsUnknownFieldsAndUnsafeReferences(t *testing.T) {
	payload := requestPayload(t, []byte("book"), ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT)
	withUnknown := protowire.AppendTag(append([]byte(nil), payload...), 99, protowire.VarintType)
	withUnknown = protowire.AppendVarint(withUnknown, 1)
	if _, err := DecodeRequest(withUnknown, 1<<20); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown field error = %v", err)
	}
	var request ingestionv1.BookContentSelectionRequestedV1
	if err := proto.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	request.SourceReference = "originals/nested/private.pdf"
	payload, _ = proto.Marshal(&request)
	if _, err := DecodeRequest(payload, 1<<20); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe reference error = %v", err)
	}
}

func TestClassifyProtectsMixedAndChapterContent(t *testing.T) {
	longBody := strings.Repeat("meaningful body word ", 200)
	document := layout.Document{SchemaVersion: "v1", Locations: []layout.Location{
		location(1, "section_header", "Chapter One"),
		{Ordinal: 2, Items: []layout.Item{item("title", "Contents"), item("document_index", "I .... 1"), item("paragraph", longBody)}},
	}}
	if candidates := Classify(document); len(candidates) != 1 || !candidates[0].Keep {
		t.Fatalf("protected classification = %+v", candidates)
	}
}

func TestClassifyApprovedExclusionReasons(t *testing.T) {
	tests := []struct {
		name    string
		ordinal uint32
		items   []layout.Item
		reason  selection.Reason
	}{
		{name: "copyright", ordinal: 2, items: []layout.Item{item("paragraph", "Copyright 2026 Example Press. All rights reserved. ISBN 123")}, reason: selection.ReasonCopyright},
		{name: "contents", ordinal: 3, items: []layout.Item{item("section_header", "Contents"), item("document_index", "Chapter One .... 1")}, reason: selection.ReasonTableOfContents},
		{name: "figures", ordinal: 4, items: []layout.Item{item("section_header", "List of Figures"), item("document_index", "Figure One .... 2")}, reason: selection.ReasonListOfFiguresTables},
		{name: "index", ordinal: 20, items: []layout.Item{item("section_header", "Index"), item("document_index", "Architecture 10")}, reason: selection.ReasonIndex},
		{name: "catalog", ordinal: 19, items: []layout.Item{item("section_header", "Other Books")}, reason: selection.ReasonPublisherCatalog},
		{name: "also by", ordinal: 2, items: []layout.Item{item("section_header", "Also by Example Author")}, reason: selection.ReasonAlsoBy},
		{name: "colophon", ordinal: 20, items: []layout.Item{item("section_header", "Colophon")}, reason: selection.ReasonColophon},
		{name: "dedication", ordinal: 2, items: []layout.Item{item("title", "To Alice")}, reason: selection.ReasonDedicationOrnamental},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locations := make([]layout.Location, 20)
			for index := range locations {
				locations[index].Ordinal = uint32(index + 1)
			}
			locations[test.ordinal-1].Items = test.items
			candidates := Classify(layout.Document{SchemaVersion: "v1", Locations: locations})
			if len(candidates) != 1 || candidates[0].Reason != test.reason || candidates[0].Keep || len(candidates[0].Signals) < 2 {
				t.Fatalf("classification = %+v", candidates)
			}
		})
	}
}

func newTestService(t *testing.T, source SourceReader, analyzer Analyzer) *Service {
	t.Helper()
	service, err := NewService(source, analyzer, Config{
		MaximumSourceBytes: 1 << 20, MaximumRanges: 256, MaximumExcludedRatio: 0.25, MinimumSignals: 2,
		PolicyVersion: "layout-selector-v1", ParserVersion: "docling-serve-v1.21.0",
		ModelSHA256: strings.Repeat("ab", sha256.Size), ParserTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func requestPayload(t *testing.T, source []byte, mode ingestionv1.ContentSelectionMode) []byte {
	t.Helper()
	digest := sha256.Sum256(source)
	profile := sha256.Sum256([]byte("profile"))
	policy := selection.Policy{Version: "layout-selector-v1", MinimumSignals: 2, MaximumRanges: 256, MaximumExcludedRatio: 0.25}.Digest()
	message := &ingestionv1.BookContentSelectionRequestedV1{
		EventId: "request-1", RequestId: "request-1", JobId: "job-1", BookId: "book-1",
		SourceReference: "originals/AAAAAAAAAAAAAAAAAAAAAA.pdf", MediaType: "application/pdf",
		SourceSha256: digest[:], SourceByteSize: int64(len(source)), LifecycleVersion: 1,
		ProcessingProfileDigest: profile[:], Mode: mode, SelectorVersion: "layout-selector-v1",
		ParserProfile: "docling-serve-v1.21.0", CorrelationId: "correlation-1",
		OccurredAt: timestamppb.New(time.Now().UTC()), CausationId: "upload-1", Producer: "ingestion-service",
		SchemaVersion: "v1", IdempotencyKey: "request-1", PolicyDigest: policy[:],
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func location(ordinal uint32, label, text string) layout.Location {
	return layout.Location{Ordinal: ordinal, Items: []layout.Item{item(label, text)}}
}

func item(label, text string) layout.Item {
	return layout.Item{Label: label, ContentLayer: "body", Text: text}
}
