package application

import (
	"context"
	"errors"
	"slices"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

type QueryAnalyzer interface {
	Analyze(domain.SearchQuery) RetrievalPlan
}

type ChunkRetriever interface {
	Retrieve(context.Context, domain.SearchQuery, []float32, RetrievalPlan) ([]Evidence, error)
}

type DocumentRetriever interface {
	Retrieve(context.Context, domain.SearchQuery, []float32, RetrievalPlan) ([]DocumentResult, error)
}

type CandidateFusion interface {
	Fuse(domain.SearchQuery, []Evidence, []DocumentResult) []Evidence
}

type RetrievalPlan struct {
	ChunkCandidateBudget int
	ChunkPageLimit       int
	DocumentLimit        int
}

type heuristicQueryAnalyzer struct {
	policy SearchPolicy
}

func (a heuristicQueryAnalyzer) Analyze(query domain.SearchQuery) RetrievalPlan {
	chunkPageLimit := searchPageLimit(query.Limit(), a.policy.CandidatePageMultiplier)
	chunkCandidateBudget := searchCandidateBudget(query.Limit(), a.policy.CandidatePageMultiplier)
	documentLimit := query.Limit()
	if documentLimit > a.policy.RequestPolicy.MaximumResultLimit {
		documentLimit = a.policy.RequestPolicy.MaximumResultLimit
	}
	return RetrievalPlan{
		ChunkCandidateBudget: chunkCandidateBudget,
		ChunkPageLimit:       chunkPageLimit,
		DocumentLimit:        documentLimit,
	}
}

type storeChunkRetriever struct {
	store      EvidenceStore
	visibility IndexVisibility
	policy     SearchPolicy
}

func (r storeChunkRetriever) Retrieve(ctx context.Context, query domain.SearchQuery, vector []float32, plan RetrievalPlan) ([]Evidence, error) {
	results := make([]Evidence, 0, query.Limit())
	seen := make(map[string]struct{})
	for offset := 0; offset < plan.ChunkCandidateBudget; offset += plan.ChunkPageLimit {
		candidateLimit := searchCandidateLimit(plan.ChunkPageLimit, offset, plan.ChunkCandidateBudget)
		candidates, err := r.store.Search(ctx, query, vector, candidateLimit, offset)
		if err != nil {
			return nil, errors.New("search evidence")
		}
		candidateCount := len(candidates)
		candidates = filterEvidenceByMinimumScore(candidates, r.policy.MinimumVisibleScore)
		visible, err := r.visibility.FilterIndexed(ctx, candidates)
		if err != nil {
			return nil, errors.New("validate index visibility")
		}
		for _, value := range visible {
			if value.EvidenceID == "" {
				continue
			}
			if _, found := seen[value.EvidenceID]; found {
				continue
			}
			seen[value.EvidenceID] = struct{}{}
			results = append(results, value)
		}
		if candidateCount < candidateLimit {
			break
		}
	}
	sortEvidenceByScore(results)
	return results, nil
}

type storeDocumentRetriever struct {
	store      EvidenceStore
	visibility IndexVisibility
	policy     SearchPolicy
}

func (r storeDocumentRetriever) Retrieve(ctx context.Context, query domain.SearchQuery, vector []float32, plan RetrievalPlan) ([]DocumentResult, error) {
	if plan.DocumentLimit <= 0 {
		return nil, nil
	}
	page, err := r.store.SearchDocuments(ctx, query, vector, plan.DocumentLimit, 0)
	if err != nil {
		return nil, errors.New("search documents")
	}
	if len(page.Documents) == 0 {
		return nil, nil
	}
	visible, err := r.visibility.FilterIndexedDocuments(ctx, page.Documents)
	if err != nil {
		return nil, errors.New("validate index visibility")
	}
	results := make([]DocumentResult, 0, len(visible))
	for _, document := range visible {
		document.Evidence = filterEvidenceByMinimumScore(document.Evidence, r.policy.MinimumVisibleScore)
		if len(document.Evidence) > 0 {
			evidence, filterErr := r.visibility.FilterIndexed(ctx, document.Evidence)
			if filterErr != nil {
				return nil, errors.New("validate index visibility")
			}
			sortEvidenceByScore(evidence)
			document.Evidence = evidence
		}
		results = append(results, document)
	}
	sortDocumentsByScore(results)
	return results, nil
}

type reciprocalRankFusion struct{}

func (reciprocalRankFusion) Fuse(_ domain.SearchQuery, chunkCandidates []Evidence, documents []DocumentResult) []Evidence {
	type scoredEvidence struct {
		evidence Evidence
		score    float64
	}

	const reciprocalRankK = 60

	merged := make(map[string]scoredEvidence, len(chunkCandidates)+(len(documents)*2))
	merge := func(candidate Evidence, rank int) {
		if candidate.EvidenceID == "" {
			return
		}
		score := 1.0 / float64(reciprocalRankK+rank+1)
		current, found := merged[candidate.EvidenceID]
		if !found {
			merged[candidate.EvidenceID] = scoredEvidence{evidence: candidate, score: score}
			return
		}
		current.score += score
		if candidate.Score > current.evidence.Score {
			current.evidence = candidate
		}
		merged[candidate.EvidenceID] = current
	}

	for index, candidate := range chunkCandidates {
		merge(candidate, index)
	}
	for documentRank, document := range documents {
		for evidenceRank, candidate := range document.Evidence {
			merge(candidate, len(chunkCandidates)+documentRank+evidenceRank)
		}
	}

	results := make([]Evidence, 0, len(merged))
	for _, candidate := range merged {
		results = append(results, candidate.evidence)
	}
	slices.SortStableFunc(results, func(left, right Evidence) int {
		leftScore := merged[left.EvidenceID].score
		rightScore := merged[right.EvidenceID].score
		switch {
		case leftScore > rightScore:
			return -1
		case leftScore < rightScore:
			return 1
		default:
			switch {
			case left.Score > right.Score:
				return -1
			case left.Score < right.Score:
				return 1
			default:
				return 0
			}
		}
	})
	return results
}

func filterEvidenceByMinimumScore(values []Evidence, minimumVisibleScore float64) []Evidence {
	results := make([]Evidence, 0, len(values))
	for _, value := range values {
		if value.Score < minimumVisibleScore {
			continue
		}
		results = append(results, value)
	}
	return results
}

func mergeDocumentMetadata(values []DocumentResult, authoritative []DocumentResult) []DocumentResult {
	if len(values) == 0 || len(authoritative) == 0 {
		return values
	}
	byDocumentID := make(map[string]DocumentResult, len(authoritative))
	for _, document := range authoritative {
		byDocumentID[document.DocumentID] = document
	}
	for index := range values {
		metadata, found := byDocumentID[values[index].DocumentID]
		if !found {
			continue
		}
		values[index].ChunkCount = metadata.ChunkCount
		if metadata.PageStart > 0 {
			values[index].PageStart = metadata.PageStart
		}
		if metadata.PageEnd > values[index].PageEnd {
			values[index].PageEnd = metadata.PageEnd
		}
		if metadata.Score > values[index].Score {
			values[index].Score = metadata.Score
		}
	}
	sortDocumentsByScore(values)
	return values
}

func deduplicateEvidenceByScore(values []Evidence) []Evidence {
	if len(values) == 0 {
		return nil
	}
	byID := make(map[string]Evidence, len(values))
	for _, value := range values {
		current, found := byID[value.EvidenceID]
		if !found || value.Score > current.Score {
			byID[value.EvidenceID] = value
		}
	}
	results := make([]Evidence, 0, len(byID))
	for _, value := range byID {
		results = append(results, value)
	}
	sortEvidenceByScore(results)
	return results
}

func fusedEvidenceCandidates(fusion CandidateFusion, query domain.SearchQuery, chunkCandidates []Evidence, documents []DocumentResult) []Evidence {
	if fusion != nil {
		return deduplicateEvidenceByScore(fusion.Fuse(query, chunkCandidates, documents))
	}
	results := append([]Evidence{}, chunkCandidates...)
	for _, document := range documents {
		results = append(results, document.Evidence...)
	}
	return deduplicateEvidenceByScore(results)
}

func trimDocuments(values []DocumentResult, limit int) []DocumentResult {
	if len(values) <= limit {
		return values
	}
	trimmed := append([]DocumentResult(nil), values[:limit]...)
	slices.SortStableFunc(trimmed, func(left, right DocumentResult) int {
		switch {
		case left.Score > right.Score:
			return -1
		case left.Score < right.Score:
			return 1
		default:
			return 0
		}
	})
	return trimmed
}
