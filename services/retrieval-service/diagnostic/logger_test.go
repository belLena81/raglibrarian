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
	recorder.BatchFailed("book-2", "embedding_unavailable", "embedding dependency status 503")
	recorder.Rejected("batch_queue", "retrieval.index-batch.v1", "application/x-protobuf", "invalid_event", "unexpected event type")
	recorder.RetryScheduled("batch_queue", "embedding_unavailable", "context deadline exceeded")
	recorder.StaleBatchesRecovered(2)

	value := output.String()
	assert.Contains(t, value, "retrieval manifest terminal_failure recorded")
	assert.Contains(t, value, "book_id=book-1")
	assert.Contains(t, value, "reason_code=manifest_integrity")
	assert.Contains(t, value, "retrieval batch failed")
	assert.Contains(t, value, "book_id=book-2")
	assert.Contains(t, value, "reason_detail=embedding dependency status 503")
	assert.Contains(t, value, "queue=batch_queue")
	assert.Contains(t, value, "event_type=retrieval index-batch v1")
	assert.Contains(t, value, "content_type=application/x-protobuf")
	assert.Contains(t, value, "reason_detail=unexpected event type")
	assert.Contains(t, value, "operation=batch_queue")
	assert.Contains(t, value, "reason_code=embedding_unavailable")
	assert.Contains(t, value, "reason_detail=context deadline exceeded")
	assert.Contains(t, value, "result_count=2")
}
