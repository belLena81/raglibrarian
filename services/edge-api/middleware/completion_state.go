package middleware

import (
	"context"
	"net/http"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
)

type completionStateKey struct{}

type completionState struct {
	outcome    diagnostic.RequestOutcome
	status     int
	hasOutcome bool
	hasStatus  bool
}

func setCompletionStatus(request *http.Request, status int) {
	state := requestCompletionState(request)
	if state == nil {
		return
	}
	state.status = status
	state.hasStatus = true
}

func withCompletionState(request *http.Request, state *completionState) *http.Request {
	ctx := context.WithValue(request.Context(), completionStateKey{}, state)
	return request.WithContext(ctx)
}

func requestCompletionState(request *http.Request) *completionState {
	state, _ := request.Context().Value(completionStateKey{}).(*completionState)
	return state
}

func setCompletionOutcome(request *http.Request, outcome diagnostic.RequestOutcome) {
	state := requestCompletionState(request)
	if state == nil {
		return
	}
	state.outcome = outcome
	state.hasOutcome = true
}
