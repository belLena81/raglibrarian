package application

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const summaryAssessmentProfileVersion = "retrieval-summary-assessment-v1"

type AssessmentCache interface {
	Lookup(context.Context, AssessmentCacheLookup) (EvidenceAssessment, AssessmentCacheOutcome, error)
	Store(context.Context, AssessmentCacheEntry) error
}

type AssessmentCacheObserver interface {
	AssessmentCacheLookup(AssessmentCacheOutcome)
	AssessmentCacheStore(AssessmentCacheOutcome)
}

type AssessmentCachePolicy struct {
	TTL                   time.Duration
	NegativeReuse         bool
	NegativeMinimumCosine float64
	MaximumEntries        int
	MaximumInputRunes     int
	ProviderProfile       string
}

type AssessmentCacheLookup struct {
	ProviderProfile       string
	QuestionHash          string
	PassageHash           string
	TopicTokens           []string
	GuardTokens           []string
	QueryEmbedding        []float32
	NegativeReuse         bool
	NegativeMinimumCosine float64
	Now                   time.Time
}

type AssessmentCacheEntry struct {
	ProviderProfile string
	QuestionHash    string
	PassageHash     string
	TopicTokens     []string
	GuardTokens     []string
	QueryEmbedding  []float32
	Assessment      EvidenceAssessment
	ExpiresAt       time.Time
	MaximumEntries  int
}

func (l AssessmentCacheLookup) NegativeCompatible(tokens, guards []string, embedding []float32) AssessmentCacheOutcome {
	if !sameAssessmentTokens(l.GuardTokens, guards) {
		return AssessmentCacheOutcomeGuardMismatch
	}
	if !assessmentTopicsCompatible(l.TopicTokens, tokens) {
		return AssessmentCacheOutcomeMiss
	}
	if assessmentCosine(l.QueryEmbedding, embedding) < l.NegativeMinimumCosine {
		return AssessmentCacheOutcomeSemanticMismatch
	}
	return AssessmentCacheOutcomeNegativeHit
}

func EncodeAssessmentEmbedding(value []float32) []byte {
	return encodeFloat32Vector(value)
}

func DecodeAssessmentEmbedding(data []byte) []float32 {
	return decodeFloat32Vector(data)
}

type AssessmentCacheOutcome string

const (
	AssessmentCacheOutcomeHit              AssessmentCacheOutcome = "hit"
	AssessmentCacheOutcomeNegativeHit      AssessmentCacheOutcome = "negative_hit"
	AssessmentCacheOutcomeMiss             AssessmentCacheOutcome = "miss"
	AssessmentCacheOutcomeSemanticMismatch AssessmentCacheOutcome = "semantic_mismatch"
	AssessmentCacheOutcomeGuardMismatch    AssessmentCacheOutcome = "guard_mismatch"
	AssessmentCacheOutcomeStored           AssessmentCacheOutcome = "stored"
	AssessmentCacheOutcomeLookupError      AssessmentCacheOutcome = "lookup_error"
	AssessmentCacheOutcomeStoreError       AssessmentCacheOutcome = "store_error"
)

func newAssessmentCacheLookup(policy AssessmentCachePolicy, question, passage string, embedding []float32, now time.Time) (AssessmentCacheLookup, bool) {
	if policy.TTL <= 0 || strings.TrimSpace(policy.ProviderProfile) == "" || !validCacheEmbedding(embedding) {
		return AssessmentCacheLookup{}, false
	}
	normalizedQuestion := normalizeSummaryInput(question, policy.MaximumInputRunes)
	normalizedPassage := normalizeSummaryInput(passage, policy.MaximumInputRunes)
	if normalizedQuestion == "" || normalizedPassage == "" {
		return AssessmentCacheLookup{}, false
	}
	return AssessmentCacheLookup{
		ProviderProfile:       policy.ProviderProfile,
		QuestionHash:          digestString(normalizedQuestion),
		PassageHash:           digestString(normalizedPassage),
		TopicTokens:           assessmentTopicTokens(normalizedQuestion),
		GuardTokens:           assessmentGuardTokens(normalizedQuestion),
		QueryEmbedding:        append([]float32(nil), embedding...),
		NegativeReuse:         policy.NegativeReuse,
		NegativeMinimumCosine: policy.NegativeMinimumCosine,
		Now:                   now,
	}, true
}

func newAssessmentCacheEntry(lookup AssessmentCacheLookup, assessment EvidenceAssessment, policy AssessmentCachePolicy) AssessmentCacheEntry {
	return AssessmentCacheEntry{
		ProviderProfile: lookup.ProviderProfile,
		QuestionHash:    lookup.QuestionHash,
		PassageHash:     lookup.PassageHash,
		TopicTokens:     append([]string(nil), lookup.TopicTokens...),
		GuardTokens:     append([]string(nil), lookup.GuardTokens...),
		QueryEmbedding:  append([]float32(nil), lookup.QueryEmbedding...),
		Assessment:      assessment,
		ExpiresAt:       lookup.Now.Add(policy.TTL),
		MaximumEntries:  policy.MaximumEntries,
	}
}

func AssessmentCacheProfile(baseURL, model, outputMode string, maxOutputTokens, maxInputRunes int) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%q%q%q%d%d%q", baseURL, model, outputMode, maxOutputTokens, maxInputRunes, summaryAssessmentProfileVersion)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func assessmentTopicsCompatible(left, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	leftSet := make(map[string]struct{}, len(left))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	intersection := 0
	for _, value := range right {
		if _, found := leftSet[value]; found {
			intersection++
		}
	}
	return float64(2*intersection)/float64(len(left)+len(right)) >= 0.8
}

func assessmentTopicTokens(value string) []string {
	ignored := map[string]struct{}{
		"a": {}, "an": {}, "about": {}, "and": {}, "are": {}, "can": {}, "could": {}, "does": {}, "for": {}, "from": {},
		"how": {}, "in": {}, "is": {}, "of": {}, "on": {}, "the": {}, "to": {}, "what": {}, "when": {}, "where": {}, "why": {},
	}
	unique := make(map[string]struct{})
	for _, word := range assessmentWords(value) {
		if _, skip := ignored[word]; skip {
			continue
		}
		unique[word] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for word := range unique {
		result = append(result, word)
	}
	sort.Strings(result)
	return result
}

func assessmentGuardTokens(value string) []string {
	unique := make(map[string]struct{})
	for _, word := range assessmentWords(value) {
		if strings.IndexFunc(word, unicode.IsDigit) >= 0 {
			unique[word] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for word := range unique {
		result = append(result, word)
	}
	sort.Strings(result)
	return result
}

func assessmentWords(value string) []string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			builder.WriteRune(char)
		} else {
			builder.WriteByte(' ')
		}
	}
	return strings.Fields(builder.String())
}

func sameAssessmentTokens(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assessmentCosine(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return -1
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		l, r := float64(left[index]), float64(right[index])
		if math.IsNaN(l) || math.IsInf(l, 0) || math.IsNaN(r) || math.IsInf(r, 0) {
			return -1
		}
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return -1
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func validCacheEmbedding(value []float32) bool { return assessmentCosine(value, value) > 0 }

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func encodeFloat32Vector(value []float32) []byte {
	data := make([]byte, len(value)*4)
	for index, item := range value {
		binary.BigEndian.PutUint32(data[index*4:], math.Float32bits(item))
	}
	return data
}

func decodeFloat32Vector(data []byte) []float32 {
	if len(data)%4 != 0 {
		return nil
	}
	result := make([]float32, len(data)/4)
	for index := range result {
		result[index] = math.Float32frombits(binary.BigEndian.Uint32(data[index*4:]))
	}
	return result
}
