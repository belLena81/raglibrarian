package runtime

import "testing"

func TestProviderRequestsPerMinuteUsesConfiguredOverride(t *testing.T) {
	if got := providerRequestsPerMinute("model:free", 22); got != 22 {
		t.Fatalf("providerRequestsPerMinute() = %d, want 22", got)
	}
}

func TestProviderRequestsPerMinuteAppliesFreeTierDefaultInRuntime(t *testing.T) {
	if got := providerRequestsPerMinute("inclusionai/ling-3.0-flash:free", 0); got != 15 {
		t.Fatalf("providerRequestsPerMinute() = %d, want 15", got)
	}
}

func TestProviderRequestsPerMinuteLeavesUnsetNonFreeModelUnlimited(t *testing.T) {
	if got := providerRequestsPerMinute("model", 0); got != 0 {
		t.Fatalf("providerRequestsPerMinute() = %d, want 0", got)
	}
}
