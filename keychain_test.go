package main

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestOpenRouterAPIKeyRoundTrip(t *testing.T) {
	keyring.MockInit()

	if _, err := loadOpenRouterAPIKey(); err != nil {
		t.Fatalf("loadOpenRouterAPIKey on empty keychain returned error: %v", err)
	}

	if err := saveOpenRouterAPIKey("sk-or-test-123"); err != nil {
		t.Fatalf("saveOpenRouterAPIKey returned error: %v", err)
	}
	got, err := loadOpenRouterAPIKey()
	if err != nil {
		t.Fatalf("loadOpenRouterAPIKey returned error: %v", err)
	}
	if got != "sk-or-test-123" {
		t.Fatalf("loadOpenRouterAPIKey = %q, want sk-or-test-123", got)
	}

	if err := clearOpenRouterAPIKey(); err != nil {
		t.Fatalf("clearOpenRouterAPIKey returned error: %v", err)
	}
	got, err = loadOpenRouterAPIKey()
	if err != nil {
		t.Fatalf("loadOpenRouterAPIKey after clear returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("loadOpenRouterAPIKey after clear = %q, want empty", got)
	}
}

func TestClearOpenRouterAPIKeyIsIdempotent(t *testing.T) {
	keyring.MockInit()
	if err := clearOpenRouterAPIKey(); err != nil {
		t.Fatalf("clearOpenRouterAPIKey on empty keychain returned error: %v", err)
	}
}
