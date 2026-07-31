package selection

import (
	"math"
	"testing"
)

func TestDecideExcludesOnlyCorroboratedUnambiguousLocations(t *testing.T) {
	policy := testPolicy()
	result := Decide(policy, 12, []Candidate{
		{Ordinal: 1, Reason: ReasonTitle, Signals: []Signal{SignalPosition, SignalLayout}},
		{Ordinal: 2, Reason: ReasonTitle, Signals: []Signal{SignalPosition, SignalLayout}},
		{Ordinal: 3, Reason: ReasonCopyright, Signals: []Signal{SignalPosition}},
		{Ordinal: 4, Reason: ReasonTableOfContents, Signals: []Signal{SignalPosition, SignalHierarchy}, Keep: true},
		{Ordinal: 5, Reason: ReasonIndex, Signals: []Signal{SignalPosition, SignalContentShape}},
		{Ordinal: 5, Reason: ReasonColophon, Signals: []Signal{SignalLayout, SignalContentShape}},
	})

	if result.Fallback != FallbackNone {
		t.Fatalf("fallback = %q", result.Fallback)
	}
	want := []Range{{Start: 1, End: 2, Reason: ReasonTitle}}
	assertRanges(t, result.Ranges, want)
}

func TestPolicyDigestCoversEveryDecisionBound(t *testing.T) {
	base := testPolicy()
	baseDigest := base.Digest()
	variants := []Policy{base, base, base, base}
	variants[0].Version = "layout-selector-v2"
	variants[1].MinimumSignals++
	variants[2].MaximumRanges++
	variants[3].MaximumExcludedRatio = 0.5
	for index, variant := range variants {
		if variant.Digest() == baseDigest {
			t.Fatalf("variant %d did not change policy digest", index)
		}
	}
}

func TestDecideSupportsEveryApprovedReason(t *testing.T) {
	reasons := []Reason{
		ReasonTitle,
		ReasonCopyright,
		ReasonDedicationOrnamental,
		ReasonTableOfContents,
		ReasonListOfFiguresTables,
		ReasonIndex,
		ReasonPublisherCatalog,
		ReasonAlsoBy,
		ReasonColophon,
	}
	candidates := make([]Candidate, len(reasons))
	for index, reason := range reasons {
		candidates[index] = Candidate{
			Ordinal: uint32(index + 1),
			Reason:  reason,
			Signals: []Signal{SignalPosition, SignalLayout},
		}
	}
	policy := testPolicy()
	policy.MaximumExcludedRatio = 1
	result := Decide(policy, uint32(len(candidates)), candidates)
	if result.Fallback != FallbackNone || len(result.Ranges) != len(reasons) {
		t.Fatalf("result = %#v", result)
	}
	for index, reason := range reasons {
		if result.Ranges[index].Reason != reason {
			t.Fatalf("range %d reason = %q", index, result.Ranges[index].Reason)
		}
	}
}

func TestDecideCoalescesOnlyAdjacentRangesWithSameReason(t *testing.T) {
	policy := testPolicy()
	policy.MaximumExcludedRatio = 1
	result := Decide(policy, 8, []Candidate{
		{Ordinal: 4, Reason: ReasonIndex, Signals: []Signal{SignalPosition, SignalContentShape}},
		{Ordinal: 2, Reason: ReasonTitle, Signals: []Signal{SignalPosition, SignalLayout}},
		{Ordinal: 1, Reason: ReasonTitle, Signals: []Signal{SignalLayout, SignalPosition}},
		{Ordinal: 3, Reason: ReasonCopyright, Signals: []Signal{SignalPosition, SignalHierarchy}},
		{Ordinal: 5, Reason: ReasonIndex, Signals: []Signal{SignalPosition, SignalContentShape}},
		{Ordinal: 7, Reason: ReasonIndex, Signals: []Signal{SignalPosition, SignalContentShape}},
	})
	want := []Range{
		{Start: 1, End: 2, Reason: ReasonTitle},
		{Start: 3, End: 3, Reason: ReasonCopyright},
		{Start: 4, End: 5, Reason: ReasonIndex},
		{Start: 7, End: 7, Reason: ReasonIndex},
	}
	assertRanges(t, result.Ranges, want)
}

func TestDecideCombinesIndependentSignalsForSameReason(t *testing.T) {
	policy := testPolicy()
	result := Decide(policy, 10, []Candidate{
		{Ordinal: 1, Reason: ReasonTitle, Signals: []Signal{SignalPosition}},
		{Ordinal: 1, Reason: ReasonTitle, Signals: []Signal{SignalLayout}},
	})
	assertRanges(t, result.Ranges, []Range{{Start: 1, End: 1, Reason: ReasonTitle}})
}

func TestDecideFailsOpenOnInvalidInputOrPolicyBounds(t *testing.T) {
	tests := []struct {
		name       string
		policy     Policy
		total      uint32
		candidates []Candidate
		fallback   Fallback
	}{
		{name: "zero total", policy: testPolicy(), fallback: FallbackInvalidInput},
		{name: "ordinal outside source", policy: testPolicy(), total: 2, candidates: []Candidate{{Ordinal: 3, Reason: ReasonTitle, Signals: []Signal{SignalPosition, SignalLayout}}}, fallback: FallbackInvalidInput},
		{name: "unknown reason", policy: testPolicy(), total: 2, candidates: []Candidate{{Ordinal: 1, Reason: Reason("other"), Signals: []Signal{SignalPosition, SignalLayout}}}, fallback: FallbackInvalidInput},
		{name: "unknown signal", policy: testPolicy(), total: 2, candidates: []Candidate{{Ordinal: 1, Reason: ReasonTitle, Signals: []Signal{SignalPosition, Signal("other")}}}, fallback: FallbackInvalidInput},
		{name: "invalid policy", policy: Policy{Version: PolicyVersionV1}, total: 2, fallback: FallbackInvalidPolicy},
		{name: "non-finite ratio", policy: Policy{Version: PolicyVersionV1, MinimumSignals: 2, MaximumExcludedRatio: math.NaN(), MaximumRanges: 256}, total: 2, fallback: FallbackInvalidPolicy},
		{name: "ratio exceeded", policy: testPolicy(), total: 4, candidates: corroborated(1, 2), fallback: FallbackExcludedRatio},
		{name: "range count exceeded", policy: Policy{Version: PolicyVersionV1, MinimumSignals: 2, MaximumExcludedRatio: 1, MaximumRanges: 1}, total: 4, candidates: corroborated(1, 3), fallback: FallbackRangeLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Decide(test.policy, test.total, test.candidates)
			if result.Fallback != test.fallback || len(result.Ranges) != 0 {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestValidateResultRejectsMalformedAndOverPolicyRanges(t *testing.T) {
	policy := testPolicy()
	valid := Result{
		PolicyVersion:  PolicyVersionV1,
		TotalLocations: 20,
		Fallback:       FallbackNone,
		Ranges:         []Range{{Start: 1, End: 2, Reason: ReasonTitle}},
	}
	if err := ValidateResult(policy, valid); err != nil {
		t.Fatalf("valid result: %v", err)
	}

	invalid := []Result{
		{PolicyVersion: "other", TotalLocations: 20, Fallback: FallbackNone},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: Fallback("other")},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: FallbackInvalidInput, Ranges: valid.Ranges},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: FallbackNone, Ranges: []Range{{Start: 0, End: 1, Reason: ReasonTitle}}},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: FallbackNone, Ranges: []Range{{Start: 2, End: 1, Reason: ReasonTitle}}},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: FallbackNone, Ranges: []Range{{Start: 1, End: 2, Reason: ReasonTitle}, {Start: 2, End: 3, Reason: ReasonCopyright}}},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: FallbackNone, Ranges: []Range{{Start: 2, End: 2, Reason: ReasonTitle}, {Start: 1, End: 1, Reason: ReasonCopyright}}},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 20, Fallback: FallbackNone, Ranges: []Range{{Start: 20, End: 21, Reason: ReasonTitle}}},
		{PolicyVersion: PolicyVersionV1, TotalLocations: 4, Fallback: FallbackNone, Ranges: []Range{{Start: 1, End: 2, Reason: ReasonTitle}}},
	}
	for index, result := range invalid {
		if err := ValidateResult(policy, result); err == nil {
			t.Fatalf("invalid result %d was accepted: %#v", index, result)
		}
	}
}

func testPolicy() Policy {
	return Policy{
		Version:              PolicyVersionV1,
		MinimumSignals:       2,
		MaximumExcludedRatio: 0.25,
		MaximumRanges:        256,
	}
}

func corroborated(ordinals ...uint32) []Candidate {
	result := make([]Candidate, len(ordinals))
	for index, ordinal := range ordinals {
		result[index] = Candidate{Ordinal: ordinal, Reason: ReasonTitle, Signals: []Signal{SignalPosition, SignalLayout}}
	}
	return result
}

func assertRanges(t *testing.T, got, want []Range) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ranges = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("range %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}
