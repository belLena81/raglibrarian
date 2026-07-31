// Package selection applies a conservative, text-free whole-location exclusion
// policy to layout-parser classifications.
package selection

import (
	"crypto/sha256"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

const PolicyVersionV1 = "layout-selector-v1"

// Reason is an allowlisted, content-free explanation for an exclusion.
type Reason string

const (
	ReasonTitle                Reason = "title"
	ReasonCopyright            Reason = "copyright_imprint"
	ReasonDedicationOrnamental Reason = "dedication_ornamental"
	ReasonTableOfContents      Reason = "table_of_contents"
	ReasonListOfFiguresTables  Reason = "list_of_figures_tables"
	ReasonIndex                Reason = "index"
	ReasonPublisherCatalog     Reason = "publisher_catalog_advertising"
	ReasonAlsoBy               Reason = "also_by"
	ReasonColophon             Reason = "colophon"
)

// Signal identifies an independent family of evidence. Repeated evidence from
// one family counts once.
type Signal string

const (
	SignalPosition     Signal = "position"
	SignalHierarchy    Signal = "hierarchy"
	SignalLayout       Signal = "layout"
	SignalContentShape Signal = "content_shape"
)

// Fallback describes why no locations were excluded.
type Fallback string

const (
	FallbackNone          Fallback = "none"
	FallbackInvalidPolicy Fallback = "invalid_policy"
	FallbackInvalidInput  Fallback = "invalid_input"
	FallbackExcludedRatio Fallback = "excluded_ratio_exceeded"
	FallbackRangeLimit    Fallback = "range_limit_exceeded"
)

// Policy is supplied by schema-validated service configuration. Locations and
// ranges are one-based and inclusive.
type Policy struct {
	Version              string
	MinimumSignals       int
	MaximumExcludedRatio float64
	MaximumRanges        int
}

func (p Policy) Digest() [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		p.Version,
		strconv.Itoa(p.MinimumSignals),
		strconv.Itoa(p.MaximumRanges),
		strconv.FormatFloat(p.MaximumExcludedRatio, 'g', -1, 64),
	}, "\x00") + "\x00"))
}

// Candidate is a parser classification for one original page or spine item.
// Keep is a hard veto, used for protected or mixed-content locations.
type Candidate struct {
	Ordinal uint32
	Reason  Reason
	Signals []Signal
	Keep    bool
}

// Range is a coalesced set of adjacent locations with the same reason.
type Range struct {
	Start  uint32
	End    uint32
	Reason Reason
}

// Result contains no source-derived text or parser coordinates.
type Result struct {
	PolicyVersion  string
	TotalLocations uint32
	Fallback       Fallback
	Ranges         []Range
}

type locationDecision struct {
	keep    bool
	reasons map[Reason]map[Signal]struct{}
}

// Decide converts untrusted parser candidates into deterministic exclusion
// ranges. Any malformed input or exceeded safety bound fails open.
func Decide(policy Policy, totalLocations uint32, candidates []Candidate) Result {
	result := Result{
		PolicyVersion:  policy.Version,
		TotalLocations: totalLocations,
		Fallback:       FallbackNone,
	}
	if err := validatePolicy(policy); err != nil {
		result.Fallback = FallbackInvalidPolicy
		return result
	}
	if totalLocations == 0 {
		result.Fallback = FallbackInvalidInput
		return result
	}

	decisions := make(map[uint32]*locationDecision, len(candidates))
	for _, candidate := range candidates {
		if candidate.Ordinal == 0 || candidate.Ordinal > totalLocations || !validReason(candidate.Reason) {
			result.Fallback = FallbackInvalidInput
			return result
		}
		decision := decisions[candidate.Ordinal]
		if decision == nil {
			decision = &locationDecision{reasons: make(map[Reason]map[Signal]struct{})}
			decisions[candidate.Ordinal] = decision
		}
		decision.keep = decision.keep || candidate.Keep
		signals := decision.reasons[candidate.Reason]
		if signals == nil {
			signals = make(map[Signal]struct{})
			decision.reasons[candidate.Reason] = signals
		}
		for _, signal := range candidate.Signals {
			if !validSignal(signal) {
				result.Fallback = FallbackInvalidInput
				return result
			}
			signals[signal] = struct{}{}
		}
	}

	ordinals := make([]uint32, 0, len(decisions))
	for ordinal, decision := range decisions {
		if decision.keep || len(decision.reasons) != 1 {
			continue
		}
		for _, signals := range decision.reasons {
			if len(signals) >= policy.MinimumSignals {
				ordinals = append(ordinals, ordinal)
			}
		}
	}
	sort.Slice(ordinals, func(i, j int) bool { return ordinals[i] < ordinals[j] })

	for _, ordinal := range ordinals {
		decision := decisions[ordinal]
		var reason Reason
		for candidateReason := range decision.reasons {
			reason = candidateReason
		}
		last := len(result.Ranges) - 1
		if last >= 0 && result.Ranges[last].Reason == reason && result.Ranges[last].End < ^uint32(0) && ordinal == result.Ranges[last].End+1 {
			result.Ranges[last].End = ordinal
			continue
		}
		result.Ranges = append(result.Ranges, Range{Start: ordinal, End: ordinal, Reason: reason})
	}

	if len(result.Ranges) > policy.MaximumRanges {
		result.Ranges = nil
		result.Fallback = FallbackRangeLimit
		return result
	}
	if excludedLocationCount(result.Ranges) > uint64(float64(totalLocations)*policy.MaximumExcludedRatio) {
		result.Ranges = nil
		result.Fallback = FallbackExcludedRatio
	}
	return result
}

// ValidateResult validates a result received across a trust boundary against
// the locally configured policy.
func ValidateResult(policy Policy, result Result) error {
	if err := validatePolicy(policy); err != nil {
		return err
	}
	if result.PolicyVersion != policy.Version || result.TotalLocations == 0 || !validFallback(result.Fallback) {
		return errors.New("invalid selection result metadata")
	}
	if result.Fallback != FallbackNone {
		if len(result.Ranges) != 0 {
			return errors.New("fallback selection contains ranges")
		}
		return nil
	}
	if len(result.Ranges) > policy.MaximumRanges {
		return errors.New("selection range limit exceeded")
	}
	for index, item := range result.Ranges {
		if item.Start == 0 || item.End < item.Start || item.End > result.TotalLocations || !validReason(item.Reason) {
			return errors.New("invalid selection range")
		}
		if index > 0 {
			previous := result.Ranges[index-1]
			if item.Start <= previous.End {
				return errors.New("selection ranges are not sorted and disjoint")
			}
			if previous.Reason == item.Reason && previous.End < ^uint32(0) && item.Start == previous.End+1 {
				return errors.New("selection ranges are not coalesced")
			}
		}
	}
	if excludedLocationCount(result.Ranges) > uint64(float64(result.TotalLocations)*policy.MaximumExcludedRatio) {
		return errors.New("selection excluded ratio exceeded")
	}
	return nil
}

func validatePolicy(policy Policy) error {
	if policy.Version != PolicyVersionV1 || policy.MinimumSignals < 2 || policy.MinimumSignals > 4 ||
		math.IsNaN(policy.MaximumExcludedRatio) || math.IsInf(policy.MaximumExcludedRatio, 0) ||
		policy.MaximumExcludedRatio <= 0 || policy.MaximumExcludedRatio > 1 || policy.MaximumRanges < 1 {
		return errors.New("invalid selection policy")
	}
	return nil
}

func validReason(reason Reason) bool {
	switch reason {
	case ReasonTitle, ReasonCopyright, ReasonDedicationOrnamental, ReasonTableOfContents,
		ReasonListOfFiguresTables, ReasonIndex, ReasonPublisherCatalog, ReasonAlsoBy, ReasonColophon:
		return true
	default:
		return false
	}
}

func validSignal(signal Signal) bool {
	switch signal {
	case SignalPosition, SignalHierarchy, SignalLayout, SignalContentShape:
		return true
	default:
		return false
	}
}

func validFallback(fallback Fallback) bool {
	switch fallback {
	case FallbackNone, FallbackInvalidPolicy, FallbackInvalidInput, FallbackExcludedRatio, FallbackRangeLimit:
		return true
	default:
		return false
	}
}

func excludedLocationCount(ranges []Range) uint64 {
	var result uint64
	for _, item := range ranges {
		result += uint64(item.End) - uint64(item.Start) + 1
	}
	return result
}
