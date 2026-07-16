package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type commitAwareResponseWriter interface {
	chimiddleware.WrapResponseWriter
	Committed() bool
	ImplicitStatus() (int, bool)
}

type commitTrackingWriter struct {
	chimiddleware.WrapResponseWriter
	committed         bool
	implicitStatus    int
	hasImplicitStatus bool
}

func newCommitTrackingWriter(w http.ResponseWriter, protocolMajor int) commitAwareResponseWriter {
	wrapped := chimiddleware.NewWrapResponseWriter(w, protocolMajor)
	tracker := &commitTrackingWriter{WrapResponseWriter: wrapped}
	flush := flushCapability{tracker: tracker}
	hijack := hijackCapability{tracker: tracker}
	readFrom := readerFromCapability{tracker: tracker}
	push := pushCapability{tracker: tracker}

	_, supportsFlush := wrapped.(http.Flusher)
	if protocolMajor == 2 {
		_, supportsPush := wrapped.(http.Pusher)
		switch {
		case supportsFlush && supportsPush:
			return &struct {
				*commitTrackingWriter
				flushCapability
				pushCapability
			}{tracker, flush, push}
		case supportsFlush:
			return &struct {
				*commitTrackingWriter
				flushCapability
			}{tracker, flush}
		case supportsPush:
			return &struct {
				*commitTrackingWriter
				pushCapability
			}{tracker, push}
		default:
			return tracker
		}
	}

	_, supportsHijack := wrapped.(http.Hijacker)
	_, supportsReadFrom := wrapped.(io.ReaderFrom)
	switch {
	case supportsFlush && supportsHijack && supportsReadFrom:
		return &struct {
			*commitTrackingWriter
			flushCapability
			hijackCapability
			readerFromCapability
		}{tracker, flush, hijack, readFrom}
	case supportsFlush && supportsHijack:
		return &struct {
			*commitTrackingWriter
			flushCapability
			hijackCapability
		}{tracker, flush, hijack}
	case supportsFlush && supportsReadFrom:
		return &struct {
			*commitTrackingWriter
			flushCapability
			readerFromCapability
		}{tracker, flush, readFrom}
	case supportsHijack && supportsReadFrom:
		return &struct {
			*commitTrackingWriter
			hijackCapability
			readerFromCapability
		}{tracker, hijack, readFrom}
	case supportsFlush:
		return &struct {
			*commitTrackingWriter
			flushCapability
		}{tracker, flush}
	case supportsHijack:
		return &struct {
			*commitTrackingWriter
			hijackCapability
		}{tracker, hijack}
	case supportsReadFrom:
		return &struct {
			*commitTrackingWriter
			readerFromCapability
		}{tracker, readFrom}
	default:
		return tracker
	}
}

// WriteHeader records successful explicit header commitment.
func (w *commitTrackingWriter) WriteHeader(code int) {
	w.WrapResponseWriter.WriteHeader(code)
	if w.Status() != 0 {
		w.committed = true
	}
}

// Write records implicit header commitment before delegating the body write.
func (w *commitTrackingWriter) Write(value []byte) (int, error) {
	w.committed = true
	return w.WrapResponseWriter.Write(value)
}

// Committed reports whether the response can no longer be replaced safely.
func (w *commitTrackingWriter) Committed() bool {
	return w.committed
}

// ImplicitStatus reports a status committed by an operation such as Flush.
func (w *commitTrackingWriter) ImplicitStatus() (int, bool) {
	return w.implicitStatus, w.hasImplicitStatus
}

type flushCapability struct {
	tracker *commitTrackingWriter
}

// Flush records implicit 200 commitment before delegating a response flush.
func (w flushCapability) Flush() {
	w.tracker.committed = true
	w.tracker.implicitStatus = http.StatusOK
	w.tracker.hasImplicitStatus = true
	w.tracker.WrapResponseWriter.(http.Flusher).Flush()
}

type hijackCapability struct {
	tracker *commitTrackingWriter
}

// Hijack records commitment after a connection is successfully hijacked.
func (w hijackCapability) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, readWriter, err := w.tracker.WrapResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		w.tracker.committed = true
	}
	return connection, readWriter, err
}

type readerFromCapability struct {
	tracker *commitTrackingWriter
}

// ReadFrom records implicit header commitment before streaming a response body.
func (w readerFromCapability) ReadFrom(reader io.Reader) (int64, error) {
	w.tracker.committed = true
	return w.tracker.WrapResponseWriter.(io.ReaderFrom).ReadFrom(reader)
}

type pushCapability struct {
	tracker *commitTrackingWriter
}

// Push delegates a server push without committing the current response.
func (w pushCapability) Push(target string, options *http.PushOptions) error {
	return w.tracker.WrapResponseWriter.(http.Pusher).Push(target, options)
}

var (
	_ commitAwareResponseWriter = (*commitTrackingWriter)(nil)
	_ http.Flusher              = flushCapability{}
	_ http.Hijacker             = hijackCapability{}
	_ io.ReaderFrom             = readerFromCapability{}
	_ http.Pusher               = pushCapability{}
)
