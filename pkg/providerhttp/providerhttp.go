package providerhttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

func OpenAIChatCompletionsURL(baseURL string) (*url.URL, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || len(baseURL) > 2048 || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		strings.ContainsAny(baseURL, " \t\r\n") || strings.ContainsAny(parsed.Host, " \t\r\n") {
		return nil, errors.New("invalid openai-compatible endpoint")
	}
	endpoint := *parsed
	endpoint.Path = openAIChatCompletionsPath(parsed.Hostname(), parsed.Path)
	return &endpoint, nil
}

func openAIChatCompletionsPath(host, basePath string) string {
	trimmed := strings.TrimRight(basePath, "/")
	if trimmed == "" {
		if strings.EqualFold(host, "openrouter.ai") {
			return "/api/v1/chat/completions"
		}
		return "/v1/chat/completions"
	}
	if strings.EqualFold(host, "openrouter.ai") && trimmed == "/api/v1" {
		return "/api/v1/chat/completions"
	}
	if path.Base(trimmed) == "v1" {
		return path.Join(trimmed, "chat/completions")
	}
	return path.Join(trimmed, "v1/chat/completions")
}

func NewTLSHTTPClient(caFile string, timeout time.Duration) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	if caFile != "" {
		contents, readErr := os.ReadFile(caFile) // #nosec G304 -- operator-controlled trust file.
		if readErr != nil || !pool.AppendCertsFromPEM(contents) {
			return nil, errors.New("append trust roots")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func ReadSingleLineSecret(filePath string, maximumBytes int64) (string, error) {
	if filePath == "" {
		return "", errors.New("invalid secret file")
	}
	file, err := os.Open(filePath) // #nosec G304 -- operator-controlled secret path.
	if err != nil {
		return "", errors.New("invalid secret file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	pathInfo, pathErr := os.Lstat(filePath)
	if err != nil || pathErr != nil || !info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		info.Size() < 1 || info.Size() > maximumBytes || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("invalid secret file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > maximumBytes || value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid secret file")
	}
	return value, nil
}

func SanitizeDetail(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > 160 {
		runes := []rune(value)
		value = string(runes[:160])
	}
	return value
}

func ClassifyRequestError(err error) string {
	if err == nil {
		return "provider_unavailable"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "provider_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "provider_canceled"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return ClassifyRequestError(urlErr.Err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "provider_timeout"
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "x509:"):
		return "provider_tls_error"
	case strings.Contains(text, "certificate"):
		return "provider_tls_error"
	case strings.Contains(text, "no such host") || strings.Contains(text, "lookup "):
		return "provider_dns_error"
	case strings.Contains(text, "connection refused"),
		strings.Contains(text, "connect: connection timed out"),
		strings.Contains(text, "network is unreachable"),
		strings.Contains(text, "connection reset by peer"):
		return "provider_network_error"
	default:
		return "provider_transport_error"
	}
}
