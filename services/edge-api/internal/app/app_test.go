package app

import (
	"errors"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
