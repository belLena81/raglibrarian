package indexprofile

import (
	"encoding/hex"
	"testing"
)

func TestContentSelectionProfileDigestIsStable(t *testing.T) {
	tests := []struct {
		name string
		mode ContentSelectionMode
		want string
	}{
		{name: "enforcement", mode: ContentSelectionModeEnforcement, want: "7fe03d8359781589e914918de06d7994288b052222d65dcac6a63e1830a4cf85"},
		{name: "observation", mode: ContentSelectionModeObservation, want: "9c237416cd873bca972b83c7a126c0f91cf88cbb3fec8a0c15f829816d1a8c21"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := supportedContentSelectionProfile(test.mode)
			digest := profile.Digest()
			if got := hex.EncodeToString(digest[:]); got != test.want {
				t.Fatalf("content-selection digest = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProcessingConfigProfileDigestIsStable(t *testing.T) {
	tests := []struct {
		name              string
		extractionVersion string
		selection         *ContentSelectionProfile
		want              string
	}{
		{name: "M4 PDF", extractionVersion: ExtractionPDF, want: "23a35a6f4f9485df637c85efa3e5b005858d3318d58ffab1c90a66cd4d4849e9"},
		{name: "PDF enforcement", extractionVersion: ExtractionPDFFiltered, selection: contentSelectionProfilePtr(ContentSelectionModeEnforcement), want: "3e933f2a3494a0459dffba1dab6ac10e7d5df21066df85f9c0ee2e914e339a06"},
		{name: "PDF observation", extractionVersion: ExtractionPDFFiltered, selection: contentSelectionProfilePtr(ContentSelectionModeObservation), want: "6ba67b3df6017c3cbc815d7270939718653922601948e7cb8c0d9f2868cb743c"},
		{name: "EPUB enforcement", extractionVersion: ExtractionEPUBFiltered, selection: contentSelectionProfilePtr(ContentSelectionModeEnforcement), want: "29407de6e7afe76f6cba13429c438be0d5daa6396edf89590ca1b1df1f0c6484"},
		{name: "EPUB observation", extractionVersion: ExtractionEPUBFiltered, selection: contentSelectionProfilePtr(ContentSelectionModeObservation), want: "89bb7ed49b88673b37361c2834fd50496958067d6bef8a10e47b309f7e0c8da1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := supportedProcessingConfigProfile(test.extractionVersion, test.selection)
			digest := profile.Digest()
			if got := hex.EncodeToString(digest[:]); got != test.want {
				t.Fatalf("processing config digest = %q, want %q", got, test.want)
			}
		})
	}
}

func contentSelectionProfilePtr(mode ContentSelectionMode) *ContentSelectionProfile {
	profile := supportedContentSelectionProfile(mode)
	return &profile
}

func supportedContentSelectionProfile(mode ContentSelectionMode) ContentSelectionProfile {
	return ContentSelectionProfile{
		Mode:                 mode,
		PolicyVersion:        ContentSelectionV1,
		ParserVersion:        ContentSelectionParserBBoxLayoutV1,
		ModelSHA256:          ContentSelectionModelSHA256,
		MinimumSignals:       ContentSelectionMinimumSignals,
		MaximumRanges:        ContentSelectionMaximumRanges,
		MaximumExcludedRatio: ContentSelectionMaximumExcludedRatio,
	}
}

func supportedProcessingConfigProfile(extractionVersion string, selection *ContentSelectionProfile) ProcessingConfigProfile {
	return ProcessingConfigProfile{
		ExtractionVersion:    extractionVersion,
		NormalizationVersion: NormalizationNFC,
		TokenizerVersion:     TokenizerCL100K,
		ChunkingVersion:      ChunkingChapterPageWindow,
		StructureVersion:     StructureChapterBoundary,
		MaximumTokens:        MaximumTokens,
		OverlapTokens:        OverlapTokens,
		TargetPages:          2,
		MaximumPages:         3,
		MaximumChunks:        50_000,
		ChunksPerShard:       256,
		MaximumShardBytes:    4 << 20,
		ContentSelection:     selection,
	}
}
