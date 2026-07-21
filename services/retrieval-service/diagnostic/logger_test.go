package diagnostic

import (
	"bytes"
	"testing"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrievalWorkflowLogsRetainSafeFields(t *testing.T) {
	var output bytes.Buffer
	log, err := logger.NewWithWriter(&output)
	require.NoError(t, err)

	recorder := New(log)
	recorder.ManifestTerminalFailureRecorded("book-1", "manifest_integrity")
	recorder.StaleBatchesRecovered(2)

	value := output.String()
	assert.Contains(t, value, "retrieval manifest terminal_failure recorded")
	assert.Contains(t, value, "book_id=book-1")
	assert.Contains(t, value, "reason_code=manifest_integrity")
	assert.Contains(t, value, "result_count=2")
}
