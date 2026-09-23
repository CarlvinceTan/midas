package auth

import (
	"bytes"
	"context"
	"crypto/rand"
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
	openRouterAuthorizeURL = "https://openrouter.ai/auth"
	openRouterTokenURL     = "https://openrouter.ai/api/v1/auth/keys"
)

// LoginOpenRouter performs OpenRouter's PKCE flow, which returns a permanent
// API key rather than a refreshable OAuth token.
func LoginOpenRouter(ctx context.Context, client *http.Client, openURL func(string) error) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	verifier, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	callbackURL := "http://" + listener.Addr().String() + "/oauth/callback"
	result := make(chan struct {
		code string
		err  error
	}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(response http.ResponseWriter, request *http.Request) {
		code := request.URL.Query().Get("code")
		callbackErr := error(nil)
		if message := request.URL.Query().Get("error"); message != "" {
			callbackErr = errors.New(message)
		} else if code == "" {
			callbackErr = errors.New("OpenRouter returned no authorization code")
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
			_, _ = io.WriteString(response, "Midas could not complete the OpenRouter connection.")
			return
		}
		_, _ = io.WriteString(response, "Midas is connected to OpenRouter. You can close this window.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	authorize, _ := url.Parse(openRouterAuthorizeURL)
	query := authorize.Query()
	query.Set("callback_url", callbackURL)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	authorize.RawQuery = query.Encode()
	if err := openURL(authorize.String()); err != nil {
		return "", err
	}
	var callback struct {
		code string
		err  error
	}
	select {
	case callback = <-result:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if callback.err != nil {
		return "", callback.err
	}
	payload, _ := json.Marshal(map[string]string{"code": callback.code, "code_verifier": verifier, "code_challenge_method": "S256"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterTokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var envelope struct {
		Key     string `json:"key"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "", errors.New("OpenRouter returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Key == "" {
		detail := envelope.Message
		if detail == "" {
			detail = envelope.Error
		}
		return "", fmt.Errorf("OpenRouter key exchange failed (HTTP %d): %s", response.StatusCode, detail)
	}
	return envelope.Key, nil
}

func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
