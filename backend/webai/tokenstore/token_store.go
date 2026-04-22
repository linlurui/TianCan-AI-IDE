// Package tokenstore provides persistent storage for zero-token provider credentials.
// Stores captured auth tokens in ~/.tiancan/zero_token_tokens.json.
package tokenstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/webai/types"
)

var (
	mu   sync.Mutex
	home string
)

func tiancanHome() string {
	if home != "" {
		return home
	}
	h, _ := os.UserHomeDir()
	return h
}

func tokensPath() string {
	return filepath.Join(tiancanHome(), ".tiancan", "zero_token_tokens.json")
}

// LoadAll loads all stored tokens from disk. Returns {provider_id: AuthResult}.
func LoadAll() map[string]*types.AuthResult {
	mu.Lock()
	defer mu.Unlock()

	path := tokensPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]*types.AuthResult{}
	}
	var raw map[string]*types.AuthResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]*types.AuthResult{}
	}
	return raw
}

// SaveAll saves all tokens to disk.
func SaveAll(tokens map[string]*types.AuthResult) error {
	mu.Lock()
	defer mu.Unlock()

	path := tokensPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return nil
}

// GetProviderToken returns stored auth for a specific provider, or nil if not found.
func GetProviderToken(providerID string) *types.AuthResult {
	all := LoadAll()
	return all[providerID]
}

// SaveProviderToken saves auth for a specific provider.
func SaveProviderToken(providerID string, auth *types.AuthResult) error {
	all := LoadAll()
	all[providerID] = auth
	return SaveAll(all)
}

// DeleteProviderToken deletes stored auth for a provider. Returns true if it existed.
func DeleteProviderToken(providerID string) bool {
	all := LoadAll()
	if _, ok := all[providerID]; ok {
		delete(all, providerID)
		_ = SaveAll(all)
		return true
	}
	return false
}

// ListStoredProviders returns provider IDs that have stored tokens.
func ListStoredProviders() []string {
	all := LoadAll()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	return ids
}
