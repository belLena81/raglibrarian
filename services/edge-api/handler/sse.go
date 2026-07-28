package handler

import (
	"net/http"
	"time"
)

type sseTiming struct {
	heartbeatInterval  time.Duration
	revalidateInterval time.Duration
	maximumDuration    time.Duration
	writeTimeout       time.Duration
}

// SSEPolicy defines runtime-tunable Server-Sent Events timing.
type SSEPolicy struct {
	HeartbeatInterval  time.Duration
	RevalidateInterval time.Duration
	MaximumDuration    time.Duration
	WriteTimeout       time.Duration
}

func (p SSEPolicy) timing() sseTiming {
	return sseTiming{
		heartbeatInterval:  p.HeartbeatInterval,
		revalidateInterval: p.RevalidateInterval,
		maximumDuration:    p.MaximumDuration,
		writeTimeout:       p.WriteTimeout,
	}
}

func (t sseTiming) withDefaults() sseTiming {
	if t.heartbeatInterval <= 0 || t.revalidateInterval <= 0 || t.maximumDuration <= 0 || t.writeTimeout <= 0 {
		panic("handler: SSE timing must be fully configured")
	}
	return t
}

type sseWriter struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
	writeLimit time.Duration
}

func newSSEWriter(writer http.ResponseWriter, timing sseTiming) (*sseWriter, error) {
	controller := http.NewResponseController(writer)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &sseWriter{
		writer:     writer,
		controller: controller,
		writeLimit: timing.withDefaults().writeTimeout,
	}, nil
}

func (w *sseWriter) writeFrame(frame []byte) error {
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.writeLimit)); err != nil {
		return err
	}
	_, writeErr := w.writer.Write(frame)
	flushErr := error(nil)
	if writeErr == nil {
		flushErr = w.controller.Flush()
	}
	clearErr := w.controller.SetWriteDeadline(time.Time{})
	if writeErr != nil {
		return writeErr
	}
	if flushErr != nil {
		return flushErr
	}
	return clearErr
}

func (w *sseWriter) flushHeaders() error {
	return w.writeFrame(nil)
}
