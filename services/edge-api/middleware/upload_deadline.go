package middleware

import (
	"net/http"
	"time"
)

// UploadDeadlineWithBudget extends only the connection deadline needed to
// stream a valid book upload. It must run before a handler reads the multipart
// body.
func UploadDeadlineWithBudget(uploadBudget time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			controller := http.NewResponseController(w)
			deadline := time.Now().Add(uploadBudget)
			if controller.SetReadDeadline(deadline) != nil || controller.SetWriteDeadline(deadline) != nil {
				w.Header().Set("Cache-Control", "no-store, private")
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
