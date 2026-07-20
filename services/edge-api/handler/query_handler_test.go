package handler_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	querymiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

type retrievalStub struct {
	request handler.SearchRequest
	result  handler.SearchResult
	err     error
}

func (s *retrievalStub) Search(_ context.Context, request handler.SearchRequest) (handler.SearchResult, error) {
	s.request = request
	return s.result, s.err
}

func TestQueryReturnsRetrievedEvidenceAndUsesTrustedPrincipal(t *testing.T) {
	retrieval := &retrievalStub{result: handler.SearchResult{
		Query: "How does replication work?",
		Results: []handler.Evidence{{
			EvidenceID: "evidence-1",
			ChunkID:    "chunk-1",
			Book: handler.EvidenceBook{
				ID:     "book-1",
				Title:  "Distributed Systems",
				Author: "A. Author",
				Year:   2024,
				Tags:   []string{"systems"},
			},
			Chapter:   "Replication",
			Section:   "Quorums",
			PageStart: 101,
			PageEnd:   102,
			Passage:   "A stored evidence passage.",
			Score:     0.87,
		}},
		Documents: []handler.DocumentResult{{
			DocumentID: "book-1:job-1",
			Book: handler.EvidenceBook{
				ID:     "book-1",
				Title:  "Distributed Systems",
				Author: "A. Author",
				Year:   2024,
				Tags:   []string{"systems"},
			},
			ChunkCount: 12,
			PageStart:  1,
			PageEnd:    250,
			Score:      0.79,
			Evidence: []handler.Evidence{{
				EvidenceID: "evidence-1",
				ChunkID:    "chunk-1",
				Book: handler.EvidenceBook{
					ID:     "book-1",
					Title:  "Distributed Systems",
					Author: "A. Author",
					Year:   2024,
					Tags:   []string{"systems"},
				},
				Chapter:   "Replication",
				Section:   "Quorums",
				PageStart: 101,
				PageEnd:   102,
				Passage:   "A stored evidence passage.",
				Score:     0.87,
			}},
		}},
	}}
	h := handler.NewQueryHandler(retrieval)
	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{
		"question":"How does replication work?",
		"filters":{"tags":["systems"],"author":"A. Author","year_from":2020,"year_to":2025},
		"limit":7
	}`))
	req = req.WithContext(querymiddleware.WithPrincipal(req.Context(), authflow.Principal{
		UserID: "trusted-user", Role: "reader", Status: "active",
	}))
	recorder := httptest.NewRecorder()

	h.Query(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{
		"query":"How does replication work?",
		"results":[{
			"evidence_id":"evidence-1","chunk_id":"chunk-1",
			"book":{"id":"book-1","title":"Distributed Systems","author":"A. Author","year":2024,"tags":["systems"]},
			"chapter":"Replication","section":"Quorums","pages":[101,102],
			"passage":"A stored evidence passage.","score":0.87
		}],
		"documents":[{
			"document_id":"book-1:job-1",
			"book":{"id":"book-1","title":"Distributed Systems","author":"A. Author","year":2024,"tags":["systems"]},
			"chunk_count":12,"pages":[1,250],"score":0.79,
			"evidence":[{
				"evidence_id":"evidence-1","chunk_id":"chunk-1",
				"book":{"id":"book-1","title":"Distributed Systems","author":"A. Author","year":2024,"tags":["systems"]},
				"chapter":"Replication","section":"Quorums","pages":[101,102],
				"passage":"A stored evidence passage.","score":0.87
			}]
		}]
	}`, recorder.Body.String())
	assert.Equal(t, "trusted-user", retrieval.request.Actor.UserID)
	assert.Equal(t, "reader", retrieval.request.Actor.Role)
	assert.Equal(t, "active", retrieval.request.Actor.Status)
	assert.Equal(t, 7, retrieval.request.Limit)
	assert.Equal(t, 2020, *retrieval.request.Filters.YearFrom)
}

func TestQueryReturnsSuccessfulEmptyEvidence(t *testing.T) {
	retrieval := &retrievalStub{result: handler.SearchResult{Query: "unrelated", Results: []handler.Evidence{}, Documents: []handler.DocumentResult{}}}
	h := handler.NewQueryHandler(retrieval)
	req := authenticatedQueryRequest(`{"question":"unrelated"}`)
	recorder := httptest.NewRecorder()

	h.Query(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"query":"unrelated","results":[],"documents":[]}`, recorder.Body.String())
}

func TestQueryRejectsInvalidPublicRequestsWithoutCallingRetrieval(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty question", body: `{"question":" "}`},
		{name: "question too long", body: `{"question":"` + string(bytes.Repeat([]byte("q"), 2001)) + `"}`},
		{name: "too many tags", body: `{"question":"q","filters":{"tags":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u"]}}`},
		{name: "reversed years", body: `{"question":"q","filters":{"year_from":2025,"year_to":2020}}`},
		{name: "year outside public bound", body: `{"question":"q","filters":{"year_from":10000}}`},
		{name: "limit too large", body: `{"question":"q","limit":21}`},
		{name: "unknown field", body: `{"question":"q","role":"admin"}`},
		{name: "multiple values", body: `{"question":"q"}{"question":"other"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retrieval := &retrievalStub{}
			h := handler.NewQueryHandler(retrieval)
			recorder := httptest.NewRecorder()

			h.Query(recorder, authenticatedQueryRequest(test.body))

			assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, recorder.Code)
			assert.Empty(t, retrieval.request.Question)
		})
	}
}

func TestQueryRequiresTrustedPrincipal(t *testing.T) {
	h := handler.NewQueryHandler(&retrievalStub{})
	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"question":"q"}`))
	recorder := httptest.NewRecorder()

	h.Query(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestQueryMapsRetrievalFailuresToStableSanitizedErrors(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantBody   string
	}{
		{err: handler.ErrSearchForbidden, wantStatus: http.StatusForbidden, wantBody: `{"error":"forbidden"}`},
		{err: handler.ErrInvalidSearch, wantStatus: http.StatusUnprocessableEntity, wantBody: `{"error":"invalid query"}`},
		{err: errors.New("transport details must not escape"), wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"retrieval is unavailable"}`},
	}
	for _, test := range tests {
		retrieval := &retrievalStub{err: test.err}
		h := handler.NewQueryHandler(retrieval)
		recorder := httptest.NewRecorder()

		h.Query(recorder, authenticatedQueryRequest(`{"question":"q"}`))

		assert.Equal(t, test.wantStatus, recorder.Code)
		assert.JSONEq(t, test.wantBody, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "transport details")
	}
}

func TestQueryHandlerRejectsTypedNilRetrieval(t *testing.T) {
	var retrieval *retrievalStub
	assert.Panics(t, func() { handler.NewQueryHandler(retrieval) })
}

func authenticatedQueryRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(body))
	return req.WithContext(querymiddleware.WithPrincipal(req.Context(), authflow.Principal{
		UserID: "user-1", Role: "reader", Status: "active",
	}))
}
