package middleware

import (
	"encoding/json"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// BrowserMutationGuard rejects cross-site browser mutations and can require
// one exact public origin in production. It never reflects Origin values.
func BrowserMutationGuard(publicOrigin string, enforce bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMutation(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			fetchSite := r.Header.Get("Sec-Fetch-Site")
			if fetchSite != "" && fetchSite != "same-origin" {
				writeOriginForbidden(w, r)
				return
			}
			if enforce && r.Header.Get("Origin") != publicOrigin {
				writeOriginForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func writeOriginForbidden(w http.ResponseWriter, r *http.Request) {
	type errorResponse struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Code:      "forbidden",
		Error:     "forbidden",
		RequestID: chimiddleware.GetReqID(r.Context()),
	})
}
