package lambdaadapter

import (
	"context"
	"testing"
)

func TestValidatePrivateEndpointRejectsCredentialExfiltrationTargets(t *testing.T) {
	for _, endpoint := range []string{"https://8.8.8.8", "http://user:secret@10.0.0.1", "https://10.0.0.1?key=value"} {
		if err := validatePrivateEndpoint(context.Background(), endpoint); err == nil {
			t.Fatalf("validatePrivateEndpoint(%q) error = nil", endpoint)
		}
	}
	if err := validatePrivateEndpoint(context.Background(), "https://10.0.0.1"); err != nil {
		t.Fatalf("private endpoint rejected: %v", err)
	}
}
