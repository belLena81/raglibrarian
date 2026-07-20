package app

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

type readinessStub struct {
	err   error
	calls *[]string
	name  string
}

func (s readinessStub) CheckReady(context.Context) error {
	*s.calls = append(*s.calls, s.name)
	return s.err
}

func TestReadinessIncludesRetrievalAndStopsAtFirstFailure(t *testing.T) {
	calls := []string{}
	check := readiness{
		identity:                   readinessStub{name: "identity", calls: &calls},
		catalog:                    readinessStub{name: "catalog", calls: &calls},
		retrieval:                  readinessStub{name: "retrieval", calls: &calls},
		retrievalReadinessRequired: true,
	}

	err := check.CheckReady(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, []string{"identity", "catalog", "retrieval"}, calls)

	calls = nil
	retrievalFailure := errors.New("retrieval down")
	check.retrieval = readinessStub{name: "retrieval", calls: &calls, err: retrievalFailure}
	err = check.CheckReady(context.Background())
	assert.ErrorIs(t, err, retrievalFailure)
}

func TestReadinessCanSkipRetrievalForM4OnlyStacks(t *testing.T) {
	calls := []string{}
	check := readiness{
		identity:  readinessStub{name: "identity", calls: &calls},
		catalog:   readinessStub{name: "catalog", calls: &calls},
		retrieval: readinessStub{name: "retrieval", calls: &calls, err: errors.New("retrieval is disabled")},
	}

	err := check.CheckReady(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, []string{"identity", "catalog"}, calls)
}

func TestTLSFailureClassifiesFileAccessAndMaterialErrors(t *testing.T) {
	pathFailure := tlsFailure(&os.PathError{Op: "open", Path: "/sensitive/path", Err: os.ErrPermission})
	materialFailure := tlsFailure(errors.New("sensitive certificate details"))

	assert.ErrorIs(t, pathFailure, ErrInternalTLSFilesUnreadable)
	assert.ErrorIs(t, pathFailure, os.ErrPermission)
	assert.ErrorIs(t, materialFailure, ErrInternalTLSMaterialInvalid)
}

func TestHTTPServerFailureClassifiesListenAndServeErrors(t *testing.T) {
	listenFailure := httpServerFailure(&net.OpError{Op: "listen", Net: "tcp", Err: errors.New("sensitive bind details")})
	serveFailure := httpServerFailure(&net.OpError{Op: "accept", Net: "tcp", Err: errors.New("sensitive accept details")})

	assert.ErrorIs(t, listenFailure, ErrHTTPListen)
	assert.ErrorIs(t, serveFailure, ErrHTTPServe)
}
