package auth

import (
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
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	openAICodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openAICodexTokenURL     = "https://auth.openai.com/oauth/token"
	openAICodexRedirectURL  = "http://localhost:1455/auth/callback"
)

// LoginOpenAICodex performs the ChatGPT Plus/Pro browser sign-in used by Codex.
func LoginOpenAICodex(ctx context.Context, openURL func(string) error) (OAuthCredential, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return OAuthCredential{}, "", fmt.Errorf("start OpenAI OAuth callback: %w", err)
	}
	defer listener.Close()
	verifier, err := randomURLToken(32)
	if err != nil {
		return OAuthCredential{}, "", err
	}
	state, err := randomURLToken(16)
	if err != nil {
		return OAuthCredential{}, "", err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	result := make(chan struct {
		code string
		err  error
	}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(response http.ResponseWriter, request *http.Request) {
		code := request.URL.Query().Get("code")
		callbackErr := error(nil)
		if message := request.URL.Query().Get("error"); message != "" {
			callbackErr = errors.New(message)
		} else if request.URL.Query().Get("state") != state {
			callbackErr = errors.New("OpenAI OAuth state did not match")
		} else if code == "" {
			callbackErr = errors.New("OpenAI returned no authorization code")
		}
		select {
		case result <- struct {
			code string
			err  error
		}{code: code, err: callbackErr}:
		default:
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if callbackErr != nil {
			response.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(response, "Midas could not complete the OpenAI connection.")
			return
		}
		_, _ = io.WriteString(response, "Midas is connected to OpenAI Codex. You can close this window.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	authorize, _ := url.Parse(openAICodexAuthorizeURL)
	query := authorize.Query()
	query.Set("response_type", "code")
	query.Set("client_id", openAICodexClientID)
	query.Set("redirect_uri", openAICodexRedirectURL)
	query.Set("scope", "openid profile email offline_access")
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", "pi")
	authorize.RawQuery = query.Encode()
	if err := openURL(authorize.String()); err != nil {
		return OAuthCredential{}, "", err
	}
	var callback struct {
		code string
		err  error
	}
	select {
	case callback = <-result:
	case <-ctx.Done():
		return OAuthCredential{}, "", ctx.Err()
	}
	if callback.err != nil {
		return OAuthCredential{}, "", callback.err
	}
	config := oauth2.Config{ClientID: openAICodexClientID, Endpoint: oauth2.Endpoint{AuthURL: openAICodexAuthorizeURL, TokenURL: openAICodexTokenURL}, RedirectURL: openAICodexRedirectURL, Scopes: []string{"openid", "profile", "email", "offline_access"}}
	token, err := config.Exchange(ctx, callback.code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return OAuthCredential{}, "", err
	}
	accountID := openAIAccountID(token.AccessToken)
	if accountID == "" {
		return OAuthCredential{}, "", errors.New("OpenAI token did not contain a ChatGPT account ID")
	}
	return OAuthCredential{AuthorizationURL: openAICodexAuthorizeURL, TokenURL: openAICodexTokenURL, ClientID: openAICodexClientID, Scopes: config.Scopes, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType, Expiry: token.Expiry, Flow: "openai-codex"}, accountID, nil
}

func openAIAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	return accountID
}
