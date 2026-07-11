package main

import (
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	openRouterKeyringService = "atelier-openrouter-key"
	openRouterKeyringUser    = "openrouter"
	falKeyringService        = "atelier-fal-key"
	falKeyringUser           = "fal"
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

func saveFalAPIKey(apiKey string) error {
	return keyring.Set(falKeyringService, falKeyringUser, apiKey)
}

// loadFalAPIKey returns "" with a nil error when no key has been saved yet,
// mirroring loadOpenRouterAPIKey so callers treat "not configured" and "empty"
// uniformly.
func loadFalAPIKey() (string, error) {
	key, err := keyring.Get(falKeyringService, falKeyringUser)
	if err != nil {
		if err == keyring.ErrNotFound {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(key), nil
}

func clearFalAPIKey() error {
	err := keyring.Delete(falKeyringService, falKeyringUser)
	if err != nil && err != keyring.ErrNotFound {
		return err
	}
	return nil
}
