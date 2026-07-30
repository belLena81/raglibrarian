package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const summaryAssessmentProfileVersion = "retrieval-summary-assessment-v2"

type AssessmentCache interface {
	Lookup(context.Context, AssessmentCacheLookup) (EvidenceAssessment, AssessmentCacheOutcome, error)
	Store(context.Context, AssessmentCacheEntry) error
}

type AssessmentCacheObserver interface {
	AssessmentCacheSearch(AssessmentCacheStats)
}

type AssessmentCachePolicy struct {
	TTL                    time.Duration
	NegativeReuse          bool
	NegativeMinimumCosine  float64
	MaximumEntries         int
	MaximumInputRunes      int
	NegativeCandidateLimit int
	ProviderProfile        string
	HMACKey                []byte
}

type AssessmentCacheLookup struct {
	ProviderProfile        string
	QuestionHash           string
	PassageHash            string
	TopicHash              string
	GuardHash              string
	TopicTokens            []string
	GuardTokens            []string
	QueryEmbedding         []float32
	NegativeReuse          bool
	NegativeMinimumCosine  float64
	NegativeCandidateLimit int
	Now                    time.Time
}

type AssessmentCacheEntry struct {
	ProviderProfile string
	QuestionHash    string
	PassageHash     string
	TopicHash       string
	GuardHash       string
	QueryEmbedding  []float32
	Assessment      EvidenceAssessment
	ExpiresAt       time.Time
	MaximumEntries  int
	NegativeReuse   bool
}

func (l AssessmentCacheLookup) NegativeCompatible(tokens, guards []string, embedding []float32) AssessmentCacheOutcome {
	if !sameAssessmentTokens(l.GuardTokens, guards) {
		return AssessmentCacheOutcomeGuardMismatch
	}
	if !sameAssessmentTokens(l.TopicTokens, tokens) {
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

type AssessmentCacheStats struct {
	Hits, NegativeHits, Misses, SemanticMismatches, GuardMismatches  int
	LookupErrors, Stores, StoreErrors, ProviderCalls, LocalFallbacks int
}

func newAssessmentCacheLookup(policy AssessmentCachePolicy, question, passage string, embedding []float32, now time.Time) (AssessmentCacheLookup, bool) {
	if policy.TTL <= 0 || strings.TrimSpace(policy.ProviderProfile) == "" || len(policy.HMACKey) < 32 || !validCacheEmbedding(embedding) {
		return AssessmentCacheLookup{}, false
	}
	normalizedQuestion := normalizeSummaryInput(question, policy.MaximumInputRunes)
	normalizedPassage := normalizeSummaryInput(passage, policy.MaximumInputRunes)
	if normalizedQuestion == "" || normalizedPassage == "" {
		return AssessmentCacheLookup{}, false
	}
	topics := assessmentTopicTokens(normalizedQuestion)
	guards := assessmentGuardTokens(normalizedQuestion)
	return AssessmentCacheLookup{
		ProviderProfile:        policy.ProviderProfile,
		QuestionHash:           assessmentHMAC(policy.HMACKey, "question", normalizedQuestion),
		PassageHash:            assessmentHMAC(policy.HMACKey, "passage", normalizedPassage),
		TopicHash:              assessmentHMAC(policy.HMACKey, "topics", strings.Join(topics, "\x00")),
		GuardHash:              assessmentHMAC(policy.HMACKey, "guards", strings.Join(guards, "\x00")),
		TopicTokens:            topics,
		GuardTokens:            guards,
		QueryEmbedding:         append([]float32(nil), embedding...),
		NegativeReuse:          policy.NegativeReuse,
		NegativeMinimumCosine:  policy.NegativeMinimumCosine,
		NegativeCandidateLimit: policy.NegativeCandidateLimit,
		Now:                    now,
	}, true
}

func newAssessmentCacheEntry(lookup AssessmentCacheLookup, assessment EvidenceAssessment, policy AssessmentCachePolicy) AssessmentCacheEntry {
	entry := AssessmentCacheEntry{
		ProviderProfile: lookup.ProviderProfile,
		QuestionHash:    lookup.QuestionHash,
		PassageHash:     lookup.PassageHash,
		Assessment:      assessment,
		ExpiresAt:       lookup.Now.Add(policy.TTL),
		MaximumEntries:  policy.MaximumEntries,
	}
	if policy.NegativeReuse && !assessment.Relevant {
		entry.TopicHash = lookup.TopicHash
		entry.GuardHash = lookup.GuardHash
		entry.QueryEmbedding = append([]float32(nil), lookup.QueryEmbedding...)
		entry.NegativeReuse = true
	}
	return entry
}

func AssessmentCacheProfile(baseURL, model, outputMode string, maxOutputTokens, maxInputRunes, maxResponseBytes, maxSummaryBytes int) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%q%q%q%d%d%d%d%q", baseURL, model, outputMode, maxOutputTokens, maxInputRunes, maxResponseBytes, maxSummaryBytes, summaryAssessmentProfileVersion)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func assessmentHMAC(key []byte, namespace, value string) string {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(namespace))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	return fmt.Sprintf("%x", hash.Sum(nil))
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
