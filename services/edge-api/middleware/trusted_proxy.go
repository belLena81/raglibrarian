package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxyRealIP accepts forwarded addresses only from configured direct peers.
func TrustedProxyRealIP(prefixes []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			peer, parseErr := netip.ParseAddr(host)
			if err == nil && parseErr == nil && addressAllowed(peer, prefixes) {
				forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
				if forwarded == "" {
					forwarded = strings.TrimSpace(r.Header.Get("X-Real-IP"))
				}
				if client, clientErr := netip.ParseAddr(forwarded); clientErr == nil {
					r.RemoteAddr = net.JoinHostPort(client.String(), "0")
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func addressAllowed(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
