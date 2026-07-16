package middleware

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func normalizedMethod(method string) string {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func routeTemplate(r *http.Request) string {
	routeContext := chi.RouteContext(r.Context())
	if routeContext == nil {
		return "unmatched"
	}
	pattern := strings.TrimSpace(routeContext.RoutePattern())
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}
