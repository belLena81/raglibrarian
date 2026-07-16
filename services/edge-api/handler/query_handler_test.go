package handler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

func TestQueryReturnsTruthfulMilestoneResponse(t *testing.T) {
	h := handler.NewQueryHandler(diagnostic.New(zaptest.NewLogger(t)))
	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"question":"What is a goroutine?"}`))
	req = req.WithContext(qmiddleware.WithClaims(req.Context(), auth.Claims{UserID: "user-1", Role: auth.RoleReader}))
	recorder := httptest.NewRecorder()
	h.Query(recorder, req)
	assert.Equal(t, http.StatusNotImplemented, recorder.Code)
	assert.JSONEq(t, `{"error":"retrieval is unavailable in milestone 1"}`, recorder.Body.String())
}

func TestQueryRejectsEmptyQuestion(t *testing.T) {
	h := handler.NewQueryHandler(diagnostic.New(zaptest.NewLogger(t)))
	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"question":" "}`))
	req = req.WithContext(qmiddleware.WithClaims(req.Context(), auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()
	h.Query(recorder, req)
	assert.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
}

func TestQueryHandlerRejectsTypedNilDiagnostics(t *testing.T) {
	var diagnostics *diagnostic.Recorder
	assert.Panics(t, func() { handler.NewQueryHandler(diagnostics) })
}
