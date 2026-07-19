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

var defaultSSETiming = sseTiming{
	heartbeatInterval:  15 * time.Second,
	revalidateInterval: 15 * time.Second,
	maximumDuration:    5 * time.Minute,
	writeTimeout:       5 * time.Second,
}

func (t sseTiming) withDefaults() sseTiming {
	if t.heartbeatInterval <= 0 {
		t.heartbeatInterval = defaultSSETiming.heartbeatInterval
	}
	if t.revalidateInterval <= 0 {
		t.revalidateInterval = defaultSSETiming.revalidateInterval
	}
	if t.maximumDuration <= 0 {
		t.maximumDuration = defaultSSETiming.maximumDuration
	}
	if t.writeTimeout <= 0 {
		t.writeTimeout = defaultSSETiming.writeTimeout
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
