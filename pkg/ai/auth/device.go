package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceProvider describes a built-in RFC 8628 account login supported by
// Midas. Endpoints and public client IDs match the upstream pi-ai providers.
type DeviceProvider struct {
	ID              string
	Name            string
	Description     string
	ClientID        string
	Scope           string
	DeviceURL       string
	TokenURL        string
	MintURL         string
	BaseURL         string
	API             string
	DeviceExtra     map[string]string
	DefaultInterval time.Duration
}

var deviceProviders = []DeviceProvider{
	{ID: "xai", Name: "xAI", Description: "SuperGrok or X Premium", ClientID: "b1a00492-073a-47ea-816f-4c329264a828", Scope: "openid profile email offline_access grok-cli:access api:access", DeviceURL: "https://auth.x.ai/oauth2/device/code", TokenURL: "https://auth.x.ai/oauth2/token", BaseURL: "https://api.x.ai/v1", API: "openai-responses", DeviceExtra: map[string]string{"referrer": "pi"}, DefaultInterval: 5 * time.Second},
	{ID: "kimi-coding", Name: "Kimi For Coding", Description: "Kimi Code subscription", ClientID: "17e5f671-d194-4dfb-9706-5516cb48c098", DeviceURL: "https://auth.kimi.com/api/oauth/device_authorization", TokenURL: "https://auth.kimi.com/api/oauth/token", BaseURL: "https://api.kimi.com/coding", API: "anthropic-messages", DefaultInterval: 5 * time.Second},
	{ID: "meta", Name: "Meta", Description: "Muse subscription", ClientID: "1031625952748946", DeviceURL: "https://auth.meta.com/oidc/device/authorization/", TokenURL: "https://auth.meta.com/oidc/device/token/", MintURL: "https://api.meta.ai/muse-code/key", BaseURL: "https://api.meta.ai/v1", API: "openai-responses", DefaultInterval: 5 * time.Second},
}

func DeviceProviders() []DeviceProvider {
	result := make([]DeviceProvider, len(deviceProviders))
	copy(result, deviceProviders)
	return result
}

func DeviceProviderByID(id string) (DeviceProvider, bool) {
	for _, provider := range deviceProviders {
		if provider.ID == id {
			return provider, true
		}
	}
	return DeviceProvider{}, false
}

type DeviceCode struct {
	UserCode        string
	VerificationURL string
	ExpiresIn       time.Duration
}

type deviceAuthorization struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	VerifyURL  string `json:"verification_uri"`
	Complete   string `json:"verification_uri_complete"`
	ExpiresIn  int    `json:"expires_in"`
	Interval   int    `json:"interval"`
}

type tokenEnvelope struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
	Interval     int    `json:"interval"`
}

// LoginDevice starts a browser/device-code login and polls until the user
// finishes. notify is called before polling so the TUI can display the code.
func LoginDevice(ctx context.Context, provider DeviceProvider, client *http.Client, notify func(DeviceCode) error) (OAuthCredential, error) {
	if client == nil {
		client = http.DefaultClient
	}
	values := url.Values{"client_id": {provider.ClientID}}
	if provider.Scope != "" {
		values.Set("scope", provider.Scope)
	}
	for key, value := range provider.DeviceExtra {
		values.Set(key, value)
	}
	var device deviceAuthorization
	if err := postFormJSON(ctx, client, provider.DeviceURL, values, &device); err != nil {
		return OAuthCredential{}, fmt.Errorf("%s device authorization: %w", provider.Name, err)
	}
	verificationURL := device.Complete
	if verificationURL == "" {
		verificationURL = device.VerifyURL
	}
	if device.DeviceCode == "" || device.UserCode == "" || !trustedWebURL(verificationURL) {
		return OAuthCredential{}, fmt.Errorf("%s returned an invalid device authorization", provider.Name)
	}
	expires := time.Duration(device.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 15 * time.Minute
	}
	if notify != nil {
		if err := notify(DeviceCode{UserCode: device.UserCode, VerificationURL: verificationURL, ExpiresIn: expires}); err != nil {
			return OAuthCredential{}, err
		}
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = provider.DefaultInterval
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(expires)
	for {
		if err := waitContext(ctx, interval); err != nil {
			return OAuthCredential{}, err
		}
		var token tokenEnvelope
		err := postFormJSON(ctx, client, provider.TokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {provider.ClientID},
			"device_code": {device.DeviceCode},
		}, &token)
		if err == nil && token.AccessToken != "" {
			if provider.MintURL != "" {
				return mintModelAPIKey(ctx, client, provider, token.AccessToken)
			}
			tokenType := token.TokenType
			if tokenType == "" {
				tokenType = "Bearer"
			}
			lifetime := time.Duration(token.ExpiresIn) * time.Second
			if lifetime <= 0 {
				lifetime = time.Hour
			}
			tokenExpiry := time.Now().Add(lifetime)
			return OAuthCredential{TokenURL: provider.TokenURL, ClientID: provider.ClientID, Scopes: strings.Fields(provider.Scope), AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: tokenType, Expiry: tokenExpiry, Flow: provider.ID}, nil
		}
		switch token.Error {
		case "authorization_pending":
			// Keep polling.
		case "slow_down":
			if token.Interval > 0 {
				interval = time.Duration(token.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
		case "access_denied", "authorization_denied":
			return OAuthCredential{}, fmt.Errorf("%s login was denied", provider.Name)
		case "expired_token":
			return OAuthCredential{}, fmt.Errorf("%s device code expired", provider.Name)
		default:
			if err != nil {
				return OAuthCredential{}, fmt.Errorf("%s token polling: %w", provider.Name, err)
			}
			return OAuthCredential{}, fmt.Errorf("%s token polling failed: %s", provider.Name, strings.TrimSpace(token.Error+" "+token.Description))
		}
		if time.Now().After(deadline) {
			return OAuthCredential{}, fmt.Errorf("%s device code expired", provider.Name)
		}
	}
}

func mintModelAPIKey(ctx context.Context, client *http.Client, provider DeviceProvider, identityToken string) (OAuthCredential, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.MintURL, strings.NewReader("{}"))
	if err != nil {
		return OAuthCredential{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+identityToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-version", "1.0.0")
	response, err := client.Do(request)
	if err != nil {
		return OAuthCredential{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return OAuthCredential{}, err
	}
	var envelope struct {
		APIKey  string `json:"api_key"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return OAuthCredential{}, fmt.Errorf("%s key mint returned invalid JSON", provider.Name)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.APIKey == "" {
		detail := envelope.Message
		if detail == "" {
			detail = envelope.Detail
		}
		return OAuthCredential{}, fmt.Errorf("%s key mint failed (HTTP %d): %s", provider.Name, response.StatusCode, detail)
	}
	return OAuthCredential{AccessToken: envelope.APIKey, RefreshToken: identityToken, TokenType: "Bearer", Expiry: time.Now().Add(24 * time.Hour), Flow: provider.ID}, nil
}

func postFormJSON(ctx context.Context, client *http.Client, endpoint string, values url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) > 0 && json.Unmarshal(body, target) != nil {
		return fmt.Errorf("HTTP %d returned invalid JSON", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// OAuth polling errors are useful to the caller and are commonly 400s.
		if envelope, ok := target.(*tokenEnvelope); ok && envelope.Error != "" {
			return nil
		}
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func trustedWebURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
