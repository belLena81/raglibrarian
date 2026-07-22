package main

import (
	"testing"

	"github.com/belLena81/raglibrarian/pkg/process"
)

func TestParseRunAsUsesDefaultsAndValidatesOverrides(t *testing.T) {
	t.Setenv("RUN_AS_UID", "")
	t.Setenv("RUN_AS_GID", "")
	identity, err := parseRunAs()
	if err != nil || identity != (process.Identity{UID: 65532, GID: 65532}) {
		t.Fatalf("default identity=%+v err=%v", identity, err)
	}

	t.Setenv("RUN_AS_UID", "1234")
	t.Setenv("RUN_AS_GID", "5678")
	identity, err = parseRunAs()
	if err != nil || identity != (process.Identity{UID: 1234, GID: 5678}) {
		t.Fatalf("configured identity=%+v err=%v", identity, err)
	}

	for _, value := range []string{"0", "-1", "root", "2147483648"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RUN_AS_UID", value)
			t.Setenv("RUN_AS_GID", "65532")
			if _, err := parseRunAs(); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
