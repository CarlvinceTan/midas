package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const githubCopilotClientID = "Iv1.b507a08c87ecfe98"

var copilotProxyEndpoint = regexp.MustCompile(`(?:^|;)proxy-ep=([^;]+)`)

// LoginGitHubCopilot runs GitHub's device flow and exchanges the resulting
// GitHub token for a short-lived Copilot API token.
func LoginGitHubCopilot(ctx context.Context, client *http.Client, notify func(DeviceCode) error) (OAuthCredential, string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var device deviceAuthorization
	if err := postFormJSON(ctx, client, "https://github.com/login/device/code", url.Values{"client_id": {githubCopilotClientID}, "scope": {"read:user"}}, &device); err != nil {
		return OAuthCredential{}, "", err
	}
	if device.DeviceCode == "" || device.UserCode == "" || !trustedWebURL(device.VerifyURL) {
		return OAuthCredential{}, "", errors.New("GitHub returned an invalid device authorization")
	}
	expires := time.Duration(device.ExpiresIn) * time.Second
	if notify != nil {
		if err := notify(DeviceCode{UserCode: device.UserCode, VerificationURL: device.VerifyURL, ExpiresIn: expires}); err != nil {
			return OAuthCredential{}, "", err
		}
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(expires)
	for {
		if err := waitContext(ctx, interval); err != nil {
			return OAuthCredential{}, "", err
		}
		var token tokenEnvelope
		err := postFormJSON(ctx, client, "https://github.com/login/oauth/access_token", url.Values{
			"client_id": {githubCopilotClientID}, "device_code": {device.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &token)
		if err == nil && token.AccessToken != "" {
			credential, baseURL, exchangeErr := exchangeGitHubCopilotToken(ctx, client, token.AccessToken)
			return credential, baseURL, exchangeErr
		}
		switch token.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return OAuthCredential{}, "", errors.New("GitHub Copilot login was denied")
		case "expired_token":
			return OAuthCredential{}, "", errors.New("GitHub Copilot device code expired")
		default:
			if err != nil {
				return OAuthCredential{}, "", err
			}
			return OAuthCredential{}, "", fmt.Errorf("GitHub Copilot login failed: %s", token.Error)
		}
		if time.Now().After(deadline) {
			return OAuthCredential{}, "", errors.New("GitHub Copilot device code expired")
		}
	}
}

func exchangeGitHubCopilotToken(ctx context.Context, client *http.Client, githubToken string) (OAuthCredential, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return OAuthCredential{}, "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "token "+githubToken)
	request.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
	response, err := client.Do(request)
	if err != nil {
		return OAuthCredential{}, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return OAuthCredential{}, "", err
	}
	var envelope struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return OAuthCredential{}, "", errors.New("GitHub Copilot returned invalid token JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Token == "" {
		return OAuthCredential{}, "", fmt.Errorf("GitHub Copilot token exchange failed (HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	expiry := time.Unix(envelope.ExpiresAt, 0)
	if envelope.ExpiresAt == 0 {
		expiry = time.Now().Add(25 * time.Minute)
	}
	baseURL := "https://api.individual.githubcopilot.com"
	if match := copilotProxyEndpoint.FindStringSubmatch(envelope.Token); len(match) == 2 {
		baseURL = "https://" + strings.Replace(match[1], "proxy.", "api.", 1)
	}
	credential := OAuthCredential{AccessToken: envelope.Token, RefreshToken: githubToken, TokenType: "Bearer", Expiry: expiry, Flow: "github-copilot"}
	return credential, baseURL, nil
}
