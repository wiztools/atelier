package main

import (
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	openRouterKeyringService = "atelier-openrouter-key"
	openRouterKeyringUser    = "openrouter"
)

func saveOpenRouterAPIKey(apiKey string) error {
	return keyring.Set(openRouterKeyringService, openRouterKeyringUser, apiKey)
}

// loadOpenRouterAPIKey returns "" with a nil error when no key has been
// saved yet, so callers can treat "not configured" and "empty" uniformly.
func loadOpenRouterAPIKey() (string, error) {
	key, err := keyring.Get(openRouterKeyringService, openRouterKeyringUser)
	if err != nil {
		if err == keyring.ErrNotFound {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(key), nil
}

func clearOpenRouterAPIKey() error {
	err := keyring.Delete(openRouterKeyringService, openRouterKeyringUser)
	if err != nil && err != keyring.ErrNotFound {
		return err
	}
	return nil
}
