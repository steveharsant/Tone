// Package secrets stores cloud API keys in the OS keychain (Secret Service /
// KWallet on Linux, Keychain on macOS, Credential Manager on Windows).
// Keys NEVER touch the config file, logs, or API responses — the only
// operations are set, delete, and a boolean "is one stored?".
package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "tone"

// SetAPIKey stores (or replaces) the API key for a provider.
func SetAPIKey(providerName, key string) error {
	return keyring.Set(service, providerName, key)
}

// GetAPIKey retrieves a provider's key. The caller must only ever pass it
// to the provider adapter — never serialize it anywhere else.
func GetAPIKey(providerName string) (string, error) {
	return keyring.Get(service, providerName)
}

// DeleteAPIKey removes a stored key.
func DeleteAPIKey(providerName string) error {
	err := keyring.Delete(service, providerName)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// HasAPIKey reports whether a key is stored without exposing it.
func HasAPIKey(providerName string) bool {
	_, err := keyring.Get(service, providerName)
	return err == nil
}
