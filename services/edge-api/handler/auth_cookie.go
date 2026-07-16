package handler

import "net/http"

func (h *AuthHandler) setRefreshCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.refreshCookieName(),
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   60 * 60 * 24 * 30,
	})
}

func (h *AuthHandler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.refreshCookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func (h *AuthHandler) refreshCookieName() string {
	if h.secureCookie {
		return "__Host-refresh_token"
	}
	return "refresh_token"
}
