//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"
)

func TestAddBrowserMutationHeadersOnlyForMutations(t *testing.T) {
	t.Setenv("E2E_PUBLIC_ORIGIN", "https://library.example/")

	mutation, err := http.NewRequest(http.MethodPost, "http://edge.example/auth/register", nil)
	if err != nil {
		t.Fatal(err)
	}
	addBrowserMutationHeaders(mutation)
	if got := mutation.Header.Get("Origin"); got != "https://library.example" {
		t.Fatalf("Origin = %q, want trimmed configured public origin", got)
	}
	if got := mutation.Header.Get("Sec-Fetch-Site"); got != "same-origin" {
		t.Fatalf("Sec-Fetch-Site = %q, want same-origin", got)
	}

	read, err := http.NewRequest(http.MethodGet, "http://edge.example/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	addBrowserMutationHeaders(read)
	if got := read.Header.Get("Origin"); got != "" {
		t.Fatalf("GET Origin = %q, want empty", got)
	}
	if got := read.Header.Get("Sec-Fetch-Site"); got != "" {
		t.Fatalf("GET Sec-Fetch-Site = %q, want empty", got)
	}
}

func TestPublicOriginDefaultMatchesCompose(t *testing.T) {
	t.Setenv("E2E_PUBLIC_ORIGIN", "")

	if got := publicOrigin(); got != "http://localhost:5173" {
		t.Fatalf("publicOrigin() = %q, want Compose default", got)
	}
}
