// Package auth owns Midas provider credentials independently of any backend.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	KindAPIKey = "api_key"
	KindOAuth  = "oauth"
)

type OAuthCredential struct {
	AuthorizationURL string    `json:"authorizationUrl"`
	TokenURL         string    `json:"tokenUrl"`
	ClientID         string    `json:"clientId"`
	ClientSecret     string    `json:"clientSecret,omitempty"`
	Scopes           []string  `json:"scopes,omitempty"`
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	TokenType        string    `json:"tokenType,omitempty"`
	Expiry           time.Time `json:"expiry,omitempty"`
	Flow             string    `json:"flow,omitempty"`
}

type Credential struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	APIKey    string            `json:"apiKey,omitempty"`
	APIKeys   []string          `json:"apiKeys,omitempty"`
	BaseURL   string            `json:"baseUrl,omitempty"`
	API       string            `json:"api,omitempty"`
	ProjectID string            `json:"projectId,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	OAuth     *OAuthCredential  `json:"oauth,omitempty"`
}

type fileData struct {
	LastProvider string                `json:"lastProvider,omitempty"`
	Providers    map[string]Credential `json:"providers"`
}

type Store struct {
	path string
	mu   sync.Mutex
	// refreshMu serialises OAuth refreshes, which rewrite the shared credential.
	refreshMu sync.Mutex
}

func New(configDir string) *Store { return &Store{path: filepath.Join(configDir, "auth.json")} }
func (s *Store) Path() string     { return s.path }

// readLocked reads the store for callers that only need a best-effort view. A
// file that cannot be read or parsed looks like an empty store; write paths must
// use readCheckedLocked instead, or they would overwrite an unreadable file and
// destroy saved credentials.
func (s *Store) readLocked() fileData {
	data, _ := s.readCheckedLocked()
	return data
}

func (s *Store) readCheckedLocked() (fileData, error) {
	empty := fileData{Providers: map[string]Credential{}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("auth: read %s: %w", s.path, err)
	}
	data := empty
	if err := json.Unmarshal(raw, &data); err != nil {
		return empty, fmt.Errorf("auth: parse %s: %w", s.path, err)
	}
	if data.Providers == nil {
		data.Providers = map[string]Credential{}
	}
	return data, nil
}

func (s *Store) writeLocked(data fileData) error {
	if data.Providers == nil {
		data.Providers = map[string]Credential{}
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".auth-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.path)
}

func (s *Store) Get(providerID string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, ok := s.readLocked().Providers[providerID]
	return normalizeCredential(credential), ok
}

func (s *Store) Set(providerID string, credential Credential) error {
	if providerID == "" {
		return errors.New("provider ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readCheckedLocked()
	if err != nil {
		return err
	}
	data.Providers[providerID] = normalizeCredential(credential)
	data.LastProvider = providerID
	return s.writeLocked(data)
}

// AddAPIKey appends a unique provider key and makes it active without
// discarding keys already saved for the provider.
func (s *Store) AddAPIKey(providerID string, credential Credential) (Credential, error) {
	if providerID == "" {
		return Credential{}, errors.New("provider ID is required")
	}
	credential = normalizeCredential(credential)
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readCheckedLocked()
	if err != nil {
		return Credential{}, err
	}
	if existing, ok := data.Providers[providerID]; ok && existing.Kind == KindAPIKey && credential.Kind == KindAPIKey {
		existing = normalizeCredential(existing)
		credential.APIKeys = uniqueKeys(append(existing.APIKeys, credential.APIKeys...))
		if credential.APIKey == "" {
			credential.APIKey = existing.APIKey
		}
		credential = normalizeCredential(credential)
	}
	data.Providers[providerID] = credential
	data.LastProvider = providerID
	if err := s.writeLocked(data); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func (s *Store) SetActiveAPIKey(providerID, apiKey string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readCheckedLocked()
	if err != nil {
		return Credential{}, err
	}
	credential, ok := data.Providers[providerID]
	if !ok || credential.Kind != KindAPIKey {
		return Credential{}, fmt.Errorf("no API keys saved for %s", providerID)
	}
	credential = normalizeCredential(credential)
	if !containsKey(credential.APIKeys, apiKey) {
		return Credential{}, fmt.Errorf("API key is not saved for %s", providerID)
	}
	credential.APIKey = apiKey
	data.Providers[providerID] = credential
	data.LastProvider = providerID
	if err := s.writeLocked(data); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

// DeleteAPIKey removes one saved key. Removing the final key deletes the
// provider credential and clears LastProvider when appropriate.
func (s *Store) DeleteAPIKey(providerID, apiKey string) (Credential, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readCheckedLocked()
	if err != nil {
		return Credential{}, false, err
	}
	credential, ok := data.Providers[providerID]
	if !ok || credential.Kind != KindAPIKey {
		return Credential{}, false, fmt.Errorf("no API keys saved for %s", providerID)
	}
	credential = normalizeCredential(credential)
	keys := make([]string, 0, len(credential.APIKeys))
	for _, saved := range credential.APIKeys {
		if saved != apiKey {
			keys = append(keys, saved)
		}
	}
	if len(keys) == len(credential.APIKeys) {
		return Credential{}, false, fmt.Errorf("API key is not saved for %s", providerID)
	}
	if len(keys) == 0 {
		delete(data.Providers, providerID)
		if data.LastProvider == providerID {
			data.LastProvider = ""
		}
		if err := s.writeLocked(data); err != nil {
			return Credential{}, false, err
		}
		return Credential{}, true, nil
	}
	credential.APIKeys = keys
	if credential.APIKey == apiKey || !containsKey(keys, credential.APIKey) {
		credential.APIKey = keys[0]
	}
	data.Providers[providerID] = credential
	if err := s.writeLocked(data); err != nil {
		return Credential{}, false, err
	}
	return credential, false, nil
}

func (s *Store) Delete(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readCheckedLocked()
	if err != nil {
		return err
	}
	delete(data.Providers, providerID)
	if data.LastProvider == providerID {
		data.LastProvider = ""
	}
	return s.writeLocked(data)
}

func (s *Store) ProviderIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.readLocked()
	ids := make([]string, 0, len(data.Providers))
	for id := range data.Providers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *Store) LastProvider() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked().LastProvider
}

func (s *Store) OAuthClient(ctx context.Context, providerID string) (*http.Client, error) {
	if _, ok := s.Get(providerID); !ok {
		return nil, fmt.Errorf("no OAuth credential for %s", providerID)
	}
	// The client outlives the call that built it: oauth2 keeps the context for
	// every later request and refresh, so a request-scoped context would cancel
	// the client as soon as that request finished.
	base := context.WithoutCancel(ctx)
	return oauth2.NewClient(base, oauthTokenSource{ctx: base, store: s, providerID: providerID}), nil
}

// currentOAuthToken returns the stored token when it does not need refreshing.
func (s *Store) currentOAuthToken(providerID string) (*oauth2.Token, bool) {
	credential, ok := s.Get(providerID)
	if !ok || credential.Kind != KindOAuth || credential.OAuth == nil {
		return nil, false
	}
	token := &oauth2.Token{
		AccessToken: credential.OAuth.AccessToken, RefreshToken: credential.OAuth.RefreshToken,
		TokenType: credential.OAuth.TokenType, Expiry: credential.OAuth.Expiry,
	}
	return token, token.Valid()
}

func normalizeCredential(credential Credential) Credential {
	if credential.Kind != KindAPIKey {
		credential.APIKeys = nil
		return credential
	}
	credential.APIKey = strings.TrimSpace(credential.APIKey)
	credential.APIKeys = uniqueKeys(credential.APIKeys)
	if credential.APIKey != "" && !containsKey(credential.APIKeys, credential.APIKey) {
		credential.APIKeys = append(credential.APIKeys, credential.APIKey)
	}
	if credential.APIKey == "" && len(credential.APIKeys) > 0 {
		credential.APIKey = credential.APIKeys[0]
	}
	return credential
}

func uniqueKeys(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !containsKey(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func containsKey(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type oauthTokenSource struct {
	ctx        context.Context
	store      *Store
	providerID string
}

func (s oauthTokenSource) Token() (*oauth2.Token, error) {
	return s.store.OAuthToken(s.ctx, s.providerID)
}

// OAuthToken returns a current token and persists a refresh when needed.
func (s *Store) OAuthToken(ctx context.Context, providerID string) (*oauth2.Token, error) {
	// A valid token needs no serialisation.
	if token, ok := s.currentOAuthToken(providerID); ok {
		return token, nil
	}
	// A refresh rotates the stored refresh token, so two concurrent refreshes must
	// not race: the loser would persist a token the server already replaced. A
	// caller that waited re-reads below and may find the winner's token.
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	credential, ok := s.Get(providerID)
	if !ok || credential.Kind != KindOAuth || credential.OAuth == nil {
		return nil, fmt.Errorf("no OAuth credential for %s", providerID)
	}
	oauthCredential := *credential.OAuth
	config := &oauth2.Config{
		ClientID: oauthCredential.ClientID, ClientSecret: oauthCredential.ClientSecret,
		Scopes:   append([]string(nil), oauthCredential.Scopes...),
		Endpoint: oauth2.Endpoint{AuthURL: oauthCredential.AuthorizationURL, TokenURL: oauthCredential.TokenURL},
	}
	token := &oauth2.Token{
		AccessToken: oauthCredential.AccessToken, RefreshToken: oauthCredential.RefreshToken,
		TokenType: oauthCredential.TokenType, Expiry: oauthCredential.Expiry,
	}
	if token.Valid() {
		return token, nil
	}
	if oauthCredential.Flow == "meta" {
		provider, exists := DeviceProviderByID("meta")
		if !exists {
			return nil, errors.New("meta OAuth provider is unavailable")
		}
		minted, err := mintModelAPIKey(ctx, http.DefaultClient, provider, oauthCredential.RefreshToken)
		if err != nil {
			return nil, err
		}
		next := credential
		next.OAuth = &minted
		if err := s.Set(providerID, next); err != nil {
			return nil, err
		}
		return &oauth2.Token{AccessToken: minted.AccessToken, RefreshToken: minted.RefreshToken, TokenType: minted.TokenType, Expiry: minted.Expiry}, nil
	}
	if oauthCredential.Flow == "anthropic" {
		refreshed, err := exchangeAnthropicToken(ctx, http.DefaultClient, map[string]string{"grant_type": "refresh_token", "client_id": anthropicClientID, "refresh_token": oauthCredential.RefreshToken})
		if err != nil {
			return nil, err
		}
		next := credential
		next.OAuth = &refreshed
		if err := s.Set(providerID, next); err != nil {
			return nil, err
		}
		return &oauth2.Token{AccessToken: refreshed.AccessToken, RefreshToken: refreshed.RefreshToken, TokenType: refreshed.TokenType, Expiry: refreshed.Expiry}, nil
	}
	if oauthCredential.Flow == "github-copilot" {
		refreshed, _, err := exchangeGitHubCopilotToken(ctx, http.DefaultClient, oauthCredential.RefreshToken)
		if err != nil {
			return nil, err
		}
		next := credential
		next.OAuth = &refreshed
		if err := s.Set(providerID, next); err != nil {
			return nil, err
		}
		return &oauth2.Token{AccessToken: refreshed.AccessToken, RefreshToken: refreshed.RefreshToken, TokenType: refreshed.TokenType, Expiry: refreshed.Expiry}, nil
	}
	refreshed, err := config.TokenSource(ctx, token).Token()
	if err != nil {
		return nil, err
	}
	next := credential
	nextOAuth := *next.OAuth
	nextOAuth.AccessToken = refreshed.AccessToken
	nextOAuth.RefreshToken = refreshed.RefreshToken
	nextOAuth.TokenType = refreshed.TokenType
	nextOAuth.Expiry = refreshed.Expiry
	next.OAuth = &nextOAuth
	if err := s.Set(providerID, next); err != nil {
		return nil, err
	}
	return refreshed, nil
}
