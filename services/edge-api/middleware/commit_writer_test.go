package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type basicCommitTestWriter struct {
	header http.Header
}

func (w *basicCommitTestWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*basicCommitTestWriter) Write(value []byte) (int, error) {
	return len(value), nil
}

func (*basicCommitTestWriter) WriteHeader(int) {
}

type http1CommitTestWriter struct {
	basicCommitTestWriter
}

func (*http1CommitTestWriter) Flush() {
}

func (*http1CommitTestWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

func (*http1CommitTestWriter) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(io.Discard, reader)
}

type flushOnlyCommitTestWriter struct {
	basicCommitTestWriter
}

func (*flushOnlyCommitTestWriter) Flush() {
}

type hijackOnlyCommitTestWriter struct {
	basicCommitTestWriter
}

func (*hijackOnlyCommitTestWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

type readerFromOnlyCommitTestWriter struct {
	basicCommitTestWriter
}

func (*readerFromOnlyCommitTestWriter) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(io.Discard, reader)
}

type http2CommitTestWriter struct {
	basicCommitTestWriter
}

func (*http2CommitTestWriter) Flush() {
}

func (*http2CommitTestWriter) Push(string, *http.PushOptions) error {
	return nil
}

type pushOnlyCommitTestWriter struct {
	basicCommitTestWriter
}

func (*pushOnlyCommitTestWriter) Push(string, *http.PushOptions) error {
	return nil
}

func TestCommitTrackingWriterPreservesProtocolCapabilities(t *testing.T) {
	tests := []struct {
		name          string
		writer        http.ResponseWriter
		protocolMajor int
		flush         bool
		hijack        bool
		readerFrom    bool
		push          bool
	}{
		{name: "HTTP/1 basic", writer: &basicCommitTestWriter{}, protocolMajor: 1},
		{name: "HTTP/1 flush only", writer: &flushOnlyCommitTestWriter{}, protocolMajor: 1, flush: true},
		{name: "HTTP/1 hijack only", writer: &hijackOnlyCommitTestWriter{}, protocolMajor: 1, hijack: true},
		{name: "HTTP/1 reader from only", writer: &readerFromOnlyCommitTestWriter{}, protocolMajor: 1},
		{name: "HTTP/1 full", writer: &http1CommitTestWriter{}, protocolMajor: 1, flush: true, hijack: true, readerFrom: true},
		{name: "HTTP/2 basic", writer: &basicCommitTestWriter{}, protocolMajor: 2},
		{name: "HTTP/2 flush only", writer: &flushOnlyCommitTestWriter{}, protocolMajor: 2, flush: true},
		{name: "HTTP/2 push only", writer: &pushOnlyCommitTestWriter{}, protocolMajor: 2},
		{name: "HTTP/2 full", writer: &http2CommitTestWriter{}, protocolMajor: 2, flush: true, push: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := newCommitTrackingWriter(test.writer, test.protocolMajor)

			assert.Equal(t, test.flush, implements[http.Flusher](writer))
			assert.Equal(t, test.hijack, implements[http.Hijacker](writer))
			assert.Equal(t, test.readerFrom, implements[io.ReaderFrom](writer))
			assert.Equal(t, test.push, implements[http.Pusher](writer))
			assert.NotPanics(t, func() {
				if test.flush {
					writer.(http.Flusher).Flush()
				}
				if test.hijack {
					_, _, err := writer.(http.Hijacker).Hijack()
					require.NoError(t, err)
				}
				if test.readerFrom {
					_, err := writer.(io.ReaderFrom).ReadFrom(strings.NewReader("body"))
					require.NoError(t, err)
				}
				if test.push {
					require.NoError(t, writer.(http.Pusher).Push("/asset", nil))
				}
			})
		})
	}
}

func implements[T any](value any) bool {
	_, ok := value.(T)
	return ok
}

func TestCommitTrackingWriterTracksCommitOperations(t *testing.T) {
	tests := []struct {
		name              string
		writer            func() commitAwareResponseWriter
		commit            func(commitAwareResponseWriter)
		implicitStatus    int
		hasImplicitStatus bool
	}{
		{
			name: "write header",
			writer: func() commitAwareResponseWriter {
				return newCommitTrackingWriter(&basicCommitTestWriter{}, 1)
			},
			commit: func(writer commitAwareResponseWriter) {
				writer.WriteHeader(http.StatusNoContent)
			},
		},
		{
			name: "write body",
			writer: func() commitAwareResponseWriter {
				return newCommitTrackingWriter(&basicCommitTestWriter{}, 1)
			},
			commit: func(writer commitAwareResponseWriter) {
				_, _ = writer.Write([]byte("body"))
			},
		},
		{
			name: "flush",
			writer: func() commitAwareResponseWriter {
				return newCommitTrackingWriter(&http1CommitTestWriter{}, 1)
			},
			commit: func(writer commitAwareResponseWriter) {
				writer.(http.Flusher).Flush()
			},
			implicitStatus:    http.StatusOK,
			hasImplicitStatus: true,
		},
		{
			name: "read from",
			writer: func() commitAwareResponseWriter {
				return newCommitTrackingWriter(&http1CommitTestWriter{}, 1)
			},
			commit: func(writer commitAwareResponseWriter) {
				_, _ = writer.(io.ReaderFrom).ReadFrom(strings.NewReader("body"))
			},
		},
		{
			name: "hijack",
			writer: func() commitAwareResponseWriter {
				return newCommitTrackingWriter(&http1CommitTestWriter{}, 1)
			},
			commit: func(writer commitAwareResponseWriter) {
				_, _, err := writer.(http.Hijacker).Hijack()
				require.NoError(t, err)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := test.writer()
			assert.False(t, writer.Committed())

			test.commit(writer)

			assert.True(t, writer.Committed())
			status, ok := writer.ImplicitStatus()
			assert.Equal(t, test.hasImplicitStatus, ok)
			assert.Equal(t, test.implicitStatus, status)
		})
	}
}

func TestHTTP2PushDoesNotCommitResponse(t *testing.T) {
	writer := newCommitTrackingWriter(&http2CommitTestWriter{}, 2)

	require.NoError(t, writer.(http.Pusher).Push("/asset", nil))

	assert.False(t, writer.Committed())
}
