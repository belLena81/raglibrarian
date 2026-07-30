package application

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

// CachePolicy controls the optional, in-memory final-answer cache. A zero
// capacity or TTL disables caching. Profiles distinguish runtime policies that
// can generate materially different answers from the same evidence.
type CachePolicy struct {
	Capacity                   int
	TTL                        time.Duration
	MinimumCosine              float64
	SemanticOnlyMinimumCosine  float64
	MinimumLexicalTopicOverlap float64
	GeneratorProfile           string
}

type answerCache struct {
	policy  CachePolicy
	mu      sync.Mutex
	entries []cacheEntry
}

type cacheEntry struct {
	created            time.Time
	match              queryMatch
	key                cacheKey
	evidenceProjection map[string]string
	segments           []domain.AnswerSegment
}

type cacheKey struct {
	authScope            string
	filters              string
	limit                uint32
	minimumEvidenceScore float64
	corpusSnapshot       string
	retrievalProfile     string
	generatorProfile     string
}

type cacheFlightKey struct {
	key                 cacheKey
	normalizedQuery     string
	mode                answerMode
	evidenceFingerprint string
}

type queryMatch struct {
	normalizedQuery  string
	topicTokens      []string
	guardTokens      []string
	mode             answerMode
	embedding        []float32
	embeddingProfile string
	servedModes      map[answerMode]struct{}
}

type answerMode string

const (
	answerModeOverview        answerMode = "overview"
	answerModeExamples        answerMode = "examples"
	answerModeSteps           answerMode = "steps"
	answerModeComparison      answerMode = "comparison"
	answerModeTroubleshooting answerMode = "troubleshooting"
	answerModeRisks           answerMode = "risks"
)

type CacheOutcome string

const (
	CacheOutcomeBypass              CacheOutcome = "bypass"
	CacheOutcomeHit                 CacheOutcome = "hit"
	CacheOutcomeMiss                CacheOutcome = "miss"
	CacheOutcomeStale               CacheOutcome = "stale"
	CacheOutcomeModeMismatch        CacheOutcome = "mode_mismatch"
	CacheOutcomeTopicMismatch       CacheOutcome = "topic_mismatch"
	CacheOutcomeSemanticMismatch    CacheOutcome = "semantic_mismatch"
	CacheOutcomeSemanticOnlyHit     CacheOutcome = "semantic_only_hit"
	CacheOutcomeLexicalHit          CacheOutcome = "lexical_hit"
	CacheOutcomeGuardMismatch       CacheOutcome = "guard_mismatch"
	CacheOutcomeHardMismatch        CacheOutcome = "hard_mismatch"
	CacheOutcomeEvidenceMismatch    CacheOutcome = "evidence_mismatch"
	CacheOutcomeGenerationCoalesced CacheOutcome = "generation_coalesced"
	CacheOutcomeValidationMismatch  CacheOutcome = "validation_mismatch"
)

func newAnswerCache(policy CachePolicy) (*answerCache, error) {
	if policy.Capacity == 0 || policy.TTL == 0 {
		if policy.Capacity < 0 || policy.TTL < 0 {
			return nil, errInvalidCachePolicy
		}
		return nil, nil
	}
	if policy.Capacity < 1 || policy.Capacity > 10000 || policy.TTL < time.Second ||
		policy.MinimumCosine <= 0 || policy.MinimumCosine > 1 ||
		policy.SemanticOnlyMinimumCosine < policy.MinimumCosine || policy.SemanticOnlyMinimumCosine > 1 ||
		policy.MinimumLexicalTopicOverlap <= 0 || policy.MinimumLexicalTopicOverlap > 1 ||
		strings.TrimSpace(policy.GeneratorProfile) == "" {
		return nil, errInvalidCachePolicy
	}
	return &answerCache{policy: policy, entries: make([]cacheEntry, 0, policy.Capacity)}, nil
}

// lookup runs only after the live Retrieval call and evidence selection. This
// keeps visibility and citations current even when an LLM call is skipped.
func (c *answerCache) lookup(request domain.SearchRequest, search domain.SearchResult, evidence []domain.ContextEvidence) (cacheEntry, CacheOutcome) {
	if c == nil || !cacheableSearch(search) {
		return cacheEntry{}, CacheOutcomeBypass
	}
	key := c.key(request, search, evidence)
	match := newQueryMatch(request.Question, search)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	outcome := CacheOutcomeMiss
	if len(c.entries) > 0 {
		outcome = CacheOutcomeHardMismatch
	}
	if c.pruneExpired(now) {
		outcome = CacheOutcomeStale
	}
	for index := range c.entries {
		entry := &c.entries[index]
		if entry.key != key {
			continue
		}
		matchOutcome := queriesMatch(match, entry.match, c.policy)
		if !cacheMatchHit(matchOutcome) {
			outcome = matchOutcome
			continue
		}
		if _, allowed := entry.match.servedModes[match.mode]; !allowed || !cachedSegmentsMatchEvidence(entry.segments, evidence) {
			outcome = CacheOutcomeModeMismatch
			if _, allowed := entry.match.servedModes[match.mode]; allowed {
				outcome = CacheOutcomeEvidenceMismatch
			}
			continue
		}
		if !cachedEvidenceProjectionMatches(entry.segments, entry.evidenceProjection, evidence) {
			outcome = CacheOutcomeEvidenceMismatch
			continue
		}
		matched := cloneCacheEntry(*entry)
		c.touch(index)
		return matched, matchOutcome
	}
	return cacheEntry{}, outcome
}

func (c *answerCache) store(request domain.SearchRequest, search domain.SearchResult, evidence []domain.ContextEvidence, segments []domain.AnswerSegment) {
	if c == nil || !cacheableSearch(search) {
		return
	}
	entry := cacheEntry{
		created:            time.Now(),
		key:                c.key(request, search, evidence),
		match:              newQueryMatch(request.Question, search),
		evidenceProjection: evidenceProjectionFingerprints(evidence),
		segments:           cloneSegments(segments),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.pruneExpired(entry.created)
	for index := range c.entries {
		if equivalentCacheEntry(c.entries[index], entry) {
			c.entries[index] = entry
			c.touch(index)
			return
		}
	}
	if len(c.entries) == c.policy.Capacity {
		copy(c.entries, c.entries[1:])
		c.entries = c.entries[:len(c.entries)-1]
	}
	c.entries = append(c.entries, entry)
}

func (c *answerCache) key(request domain.SearchRequest, search domain.SearchResult, evidence []domain.ContextEvidence) cacheKey {
	return cacheKey{
		authScope:            authScopeFingerprint(request.Actor),
		filters:              canonicalFilters(request.Filters),
		limit:                request.Limit,
		minimumEvidenceScore: request.MinimumEvidenceScore,
		corpusSnapshot:       search.CorpusSnapshot,
		retrievalProfile:     search.RetrievalProfile,
		generatorProfile:     c.policy.GeneratorProfile,
	}
}

func (c *answerCache) flightKey(request domain.SearchRequest, search domain.SearchResult, evidence []domain.ContextEvidence) (cacheFlightKey, bool) {
	if c == nil || !cacheableSearch(search) {
		return cacheFlightKey{}, false
	}
	match := newQueryMatch(request.Question, search)
	return cacheFlightKey{
		key:                 c.key(request, search, evidence),
		normalizedQuery:     match.normalizedQuery,
		mode:                match.mode,
		evidenceFingerprint: evidenceFingerprint(evidence),
	}, true
}

func cacheableSearch(search domain.SearchResult) bool {
	return strings.TrimSpace(search.EmbeddingProfile) != "" &&
		strings.TrimSpace(search.RetrievalProfile) != "" &&
		strings.TrimSpace(search.CorpusSnapshot) != "" &&
		validEmbedding(search.QueryEmbedding)
}

func newQueryMatch(question string, search domain.SearchResult) queryMatch {
	mode := detectAnswerMode(question)
	return queryMatch{
		normalizedQuery:  normalizeQuery(question),
		topicTokens:      topicTokens(question),
		guardTokens:      guardTokens(question),
		mode:             mode,
		embedding:        append([]float32(nil), search.QueryEmbedding...),
		embeddingProfile: search.EmbeddingProfile,
		servedModes:      map[answerMode]struct{}{mode: {}},
	}
}

func cacheMatchHit(outcome CacheOutcome) bool {
	return outcome == CacheOutcomeHit ||
		outcome == CacheOutcomeLexicalHit ||
		outcome == CacheOutcomeSemanticOnlyHit
}

func queriesMatch(current, cached queryMatch, policy CachePolicy) CacheOutcome {
	if current.embeddingProfile != cached.embeddingProfile {
		return CacheOutcomeSemanticMismatch
	}
	if !sameTokens(current.guardTokens, cached.guardTokens) {
		return CacheOutcomeGuardMismatch
	}
	cosine := cosineSimilarity(current.embedding, cached.embedding)
	if cosine < policy.MinimumCosine {
		return CacheOutcomeSemanticMismatch
	}
	if current.normalizedQuery == cached.normalizedQuery || topicContains(current.topicTokens, cached.topicTokens) {
		return CacheOutcomeHit
	}
	if topicOverlap(current.topicTokens, cached.topicTokens) >= policy.MinimumLexicalTopicOverlap {
		return CacheOutcomeLexicalHit
	}
	if cosine >= policy.SemanticOnlyMinimumCosine {
		return CacheOutcomeSemanticOnlyHit
	}
	return CacheOutcomeTopicMismatch
}

func normalizeQuery(value string) string {
	return strings.Join(queryWords(value), " ")
}

func topicTokens(value string) []string {
	modeWords := map[string]struct{}{
		"example": {}, "examples": {}, "sample": {}, "samples": {}, "code": {}, "step": {}, "steps": {},
		"compare": {}, "comparison": {}, "versus": {}, "vs": {}, "difference": {}, "tradeoff": {}, "tradeoffs": {}, "pros": {}, "cons": {},
		"troubleshoot": {}, "troubleshooting": {}, "debug": {}, "diagnose": {}, "diagnosis": {}, "fix": {}, "error": {}, "issue": {}, "problem": {},
		"risk": {}, "risks": {}, "danger": {}, "security": {}, "pitfall": {}, "pitfalls": {},
	}
	intentWords := map[string]struct{}{
		"a": {}, "an": {}, "about": {}, "and": {}, "can": {}, "could": {}, "explain": {}, "for": {}, "give": {}, "handling": {},
		"how": {}, "i": {}, "in": {}, "is": {}, "me": {}, "of": {}, "on": {}, "please": {}, "show": {}, "tell": {}, "the": {}, "to": {}, "what": {},
	}
	unique := make(map[string]struct{})
	for _, word := range queryWords(value) {
		if _, ignored := intentWords[word]; ignored {
			continue
		}
		if _, modeWord := modeWords[word]; modeWord {
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

func queryWords(value string) []string {
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

func detectAnswerMode(question string) answerMode {
	words := queryWords(question)
	has := func(values ...string) bool {
		for _, word := range words {
			for _, value := range values {
				if word == value {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("example", "examples", "sample", "samples") || hasPhrase(words, "sample", "code"):
		return answerModeExamples
	case has("step", "steps", "guide", "tutorial"):
		return answerModeSteps
	case has("compare", "comparison", "versus", "vs", "difference", "tradeoff", "tradeoffs", "pros", "cons"):
		return answerModeComparison
	case has("troubleshoot", "troubleshooting", "debug", "diagnose", "diagnosis", "fix", "error", "issue", "problem"):
		return answerModeTroubleshooting
	case has("risk", "risks", "danger", "pitfall", "pitfalls"):
		return answerModeRisks
	default:
		return answerModeOverview
	}
}

func hasPhrase(words []string, first, second string) bool {
	for index := 0; index+1 < len(words); index++ {
		if words[index] == first && words[index+1] == second {
			return true
		}
	}
	return false
}

func topicContains(left, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	leftSet := make(map[string]struct{}, len(left))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	for _, value := range right {
		if _, found := leftSet[value]; !found {
			return false
		}
	}
	return true
}

func topicOverlap(left, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
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
	return float64(2*intersection) / float64(len(left)+len(right))
}

func guardTokens(value string) []string {
	unique := make(map[string]struct{})
	for _, raw := range rawGuardWords(value) {
		normalized := strings.Join(queryWords(raw), " ")
		if normalized == "" {
			continue
		}
		if guardToken(raw) {
			unique[normalized] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for word := range unique {
		result = append(result, word)
	}
	sort.Strings(result)
	return result
}

func rawGuardWords(value string) []string {
	var words []string
	var builder strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || strings.ContainsRune("_-/.:", char) {
			builder.WriteRune(char)
			continue
		}
		if builder.Len() > 0 {
			words = append(words, builder.String())
			builder.Reset()
		}
	}
	if builder.Len() > 0 {
		words = append(words, builder.String())
	}
	return words
}

func guardToken(raw string) bool {
	word := strings.Trim(raw, "_-/.:")
	if word == "" {
		return false
	}
	hasDigit := false
	for _, char := range word {
		switch {
		case unicode.IsDigit(char):
			hasDigit = true
		}
	}
	return hasDigit
}

func sameTokens(left, right []string) bool {
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

func cosineSimilarity(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return -1
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		leftValue, rightValue := float64(left[index]), float64(right[index])
		if math.IsNaN(leftValue) || math.IsInf(leftValue, 0) || math.IsNaN(rightValue) || math.IsInf(rightValue, 0) {
			return -1
		}
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	if leftNorm == 0 || rightNorm == 0 {
		return -1
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func validEmbedding(value []float32) bool { return cosineSimilarity(value, value) > 0 }

func canonicalFilters(filters domain.SearchFilters) string {
	tags := append([]string(nil), filters.Tags...)
	sort.Strings(tags)
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d", len(tags))
	for _, tag := range tags {
		_, _ = fmt.Fprintf(hash, "%q", tag)
	}
	year := func(value *int32) string {
		if value == nil {
			return ""
		}
		return strconv.FormatInt(int64(*value), 10)
	}
	_, _ = fmt.Fprintf(hash, "%q%q%q", filters.Author, year(filters.YearFrom), year(filters.YearTo))
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func authScopeFingerprint(actor domain.Actor) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%q%q%q", actor.UserID, actor.Role, actor.Status)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func evidenceProjectionFingerprints(evidence []domain.ContextEvidence) map[string]string {
	result := make(map[string]string, len(evidence))
	for _, value := range evidence {
		if value.EvidenceID == "" {
			continue
		}
		result[value.EvidenceID] = evidenceProjectionFingerprint(value)
	}
	return result
}

func evidenceFingerprint(evidence []domain.ContextEvidence) string {
	hash := sha256.New()
	for _, value := range evidence {
		_, _ = fmt.Fprintf(hash, "%q%q", value.EvidenceID, evidenceProjectionFingerprint(value))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func evidenceProjectionFingerprint(value domain.ContextEvidence) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(
		hash,
		"%q%q%q%q%q%q%d%d",
		value.EvidenceID,
		value.Passage,
		value.Title,
		value.Author,
		value.Chapter,
		value.Section,
		value.PageStart,
		value.PageEnd,
	)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func cachedSegmentsMatchEvidence(segments []domain.AnswerSegment, evidence []domain.ContextEvidence) bool {
	allowed := make(map[string]struct{}, len(evidence))
	for _, value := range evidence {
		allowed[value.EvidenceID] = struct{}{}
	}
	for _, segment := range segments {
		if len(segment.EvidenceIDs) == 0 {
			return false
		}
		for _, evidenceID := range segment.EvidenceIDs {
			if _, found := allowed[evidenceID]; !found {
				return false
			}
		}
	}
	return true
}

func cachedEvidenceProjectionMatches(segments []domain.AnswerSegment, cached map[string]string, evidence []domain.ContextEvidence) bool {
	current := evidenceProjectionFingerprints(evidence)
	for _, segment := range segments {
		for _, evidenceID := range segment.EvidenceIDs {
			cachedHash, found := cached[evidenceID]
			if !found {
				return false
			}
			if current[evidenceID] != cachedHash {
				return false
			}
		}
	}
	return true
}

func cloneCacheEntry(value cacheEntry) cacheEntry {
	value.segments = cloneSegments(value.segments)
	value.evidenceProjection = cloneStringMap(value.evidenceProjection)
	return value
}

func equivalentCacheEntry(left, right cacheEntry) bool {
	return left.key == right.key &&
		left.match.normalizedQuery == right.match.normalizedQuery &&
		left.match.mode == right.match.mode
}

func (c *answerCache) touch(index int) {
	if index < 0 || index >= len(c.entries)-1 {
		return
	}
	entry := c.entries[index]
	copy(c.entries[index:], c.entries[index+1:])
	c.entries[len(c.entries)-1] = entry
}

func cloneSegments(values []domain.AnswerSegment) []domain.AnswerSegment {
	result := make([]domain.AnswerSegment, len(values))
	for index, value := range values {
		result[index] = domain.AnswerSegment{Text: value.Text, EvidenceIDs: append([]string(nil), value.EvidenceIDs...)}
	}
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func (c *answerCache) pruneExpired(now time.Time) bool {
	firstLive := 0
	pruned := false
	for _, entry := range c.entries {
		if now.Sub(entry.created) < c.policy.TTL {
			c.entries[firstLive] = entry
			firstLive++
		} else {
			pruned = true
		}
	}
	c.entries = c.entries[:firstLive]
	return pruned
}

var errInvalidCachePolicy = errors.New("invalid answer cache policy")
