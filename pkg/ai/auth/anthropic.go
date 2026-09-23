package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	anthropicClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicTokenURL     = "https://platform.claude.com/v1/oauth/token"
	anthropicRedirectURL  = "http://localhost:53692/callback"
	anthropicScopes       = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
)

// LoginAnthropic performs the Claude Pro/Max PKCE browser flow.
func LoginAnthropic(ctx context.Context, client *http.Client, openURL func(string) error) (OAuthCredential, error) {
	if client == nil {
		client = http.DefaultClient
	}
	listener, err := net.Listen("tcp", "127.0.0.1:53692")
	if err != nil {
		return OAuthCredential{}, fmt.Errorf("start Anthropic OAuth callback: %w", err)
	}
	defer listener.Close()
	verifier, err := randomURLToken(32)
	if err != nil {
		return OAuthCredential{}, err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	result := make(chan struct {
		code  string
		state string
		err   error
	}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(response http.ResponseWriter, request *http.Request) {
		state := request.URL.Query().Get("state")
		code := request.URL.Query().Get("code")
		callbackErr := error(nil)
		if message := request.URL.Query().Get("error"); message != "" {
			callbackErr = errors.New(message)
		} else if state != verifier {
			callbackErr = errors.New("anthropic OAuth state did not match")
		} else if code == "" {
			callbackErr = errors.New("anthropic returned no authorization code")
		}
		select {
		case result <- struct {
			code  string
			state string
			err   error
		}{code: code, state: state, err: callbackErr}:
		default:
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if callbackErr != nil {
			response.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(response, "Midas could not complete the Anthropic connection.")
			return
		}
		_, _ = io.WriteString(response, "Midas is connected to Anthropic. You can close this window.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	authorize, _ := url.Parse(anthropicAuthorizeURL)
	query := authorize.Query()
	query.Set("code", "true")
	query.Set("client_id", anthropicClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", anthropicRedirectURL)
	query.Set("scope", anthropicScopes)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", verifier)
	authorize.RawQuery = query.Encode()
	if err := openURL(authorize.String()); err != nil {
		return OAuthCredential{}, err
	}
	var callback struct {
		code  string
		state string
		err   error
	}
	select {
	case callback = <-result:
	case <-ctx.Done():
		return OAuthCredential{}, ctx.Err()
	}
	if callback.err != nil {
		return OAuthCredential{}, callback.err
	}
	return exchangeAnthropicToken(ctx, client, map[string]string{
		"grant_type": "authorization_code", "client_id": anthropicClientID, "code": callback.code,
		"state": callback.state, "redirect_uri": anthropicRedirectURL, "code_verifier": verifier,
	})
}

func exchangeAnthropicToken(ctx context.Context, client *http.Client, fields map[string]string) (OAuthCredential, error) {
	payload, _ := json.Marshal(fields)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, bytes.NewReader(payload))
	if err != nil {
		return OAuthCredential{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return OAuthCredential{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return OAuthCredential{}, err
	}
	var token tokenEnvelope
	if json.Unmarshal(body, &token) != nil {
		return OAuthCredential{}, errors.New("anthropic returned invalid OAuth JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || token.AccessToken == "" || token.RefreshToken == "" {
		return OAuthCredential{}, fmt.Errorf("anthropic token exchange failed (HTTP %d): %s", response.StatusCode, token.Description)
	}
	return OAuthCredential{TokenURL: anthropicTokenURL, ClientID: anthropicClientID, Scopes: []string{anthropicScopes}, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: "Bearer", Expiry: time.Now().Add(time.Duration(token.ExpiresIn)*time.Second - 5*time.Minute), Flow: "anthropic"}, nil
}
