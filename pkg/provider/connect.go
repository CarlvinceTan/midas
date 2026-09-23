// Package provider: connection helpers used by the /connect UI.
package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	providerauth "github.com/CarlvinceTan/midas/pkg/ai/auth"
	providercatalog "github.com/CarlvinceTan/midas/pkg/ai/catalog"
	"golang.org/x/oauth2"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

func AuthorizeOAuth(ctx context.Context, credential providerauth.OAuthCredential, openURL func(string) error) (providerauth.OAuthCredential, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return credential, fmt.Errorf("start OAuth callback: %w", err)
	}
	defer listener.Close()
	state, err := RandomOAuthValue()
	if err != nil {
		return credential, err
	}
	verifier, err := RandomOAuthValue()
	if err != nil {
		return credential, err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	redirectURL := "http://" + listener.Addr().String() + "/callback"
	config := oauth2.Config{
		ClientID: credential.ClientID, ClientSecret: credential.ClientSecret,
		Endpoint: oauth2.Endpoint{AuthURL: credential.AuthorizationURL, TokenURL: credential.TokenURL},
		Scopes:   append([]string(nil), credential.Scopes...), RedirectURL: redirectURL,
	}
	type callbackResult struct {
		code string
		err  error
	}
	result := make(chan callbackResult, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(response http.ResponseWriter, request *http.Request) {
		callback := callbackResult{}
		if request.URL.Query().Get("state") != state {
			callback.err = errors.New("OAuth state did not match")
		} else if message := request.URL.Query().Get("error"); message != "" {
			callback.err = fmt.Errorf("OAuth authorization failed: %s", message)
		} else {
			callback.code = request.URL.Query().Get("code")
			if callback.code == "" {
				callback.err = errors.New("OAuth callback did not include a code")
			}
		}
		once.Do(func() { result <- callback })
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if callback.err != nil {
			response.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(response, "Midas could not complete the connection. You can close this window.")
			return
		}
		_, _ = io.WriteString(response, "Midas is connected. You can close this window and return to the terminal.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	authorizationURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	if err := openURL(authorizationURL); err != nil {
		return credential, err
	}
	var callback callbackResult
	select {
	case callback = <-result:
	case <-ctx.Done():
		return credential, ctx.Err()
	}
	if callback.err != nil {
		return credential, callback.err
	}
	token, err := config.Exchange(ctx, callback.code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return credential, fmt.Errorf("exchange OAuth code: %w", err)
	}
	credential.AccessToken = token.AccessToken
	credential.RefreshToken = token.RefreshToken
	credential.TokenType = token.TokenType
	credential.Expiry = token.Expiry
	return credential, nil
}

func RandomOAuthValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func OpenExternalURL(value string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", value)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", value)
	default:
		command = exec.Command("xdg-open", value)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return command.Process.Release()
}

func NormalizeAzureBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	for _, suffix := range []string{"/openai/v1/responses", "/openai/v1", "/openai", "/"} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix)
			break
		}
	}
	return value + "/openai/v1"
}

func CatalogCredential(spec providercatalog.Provider, values map[string]string) (providerauth.Credential, error) {
	credential := providerauth.Credential{
		Kind: providerauth.KindAPIKey, Name: spec.Name, APIKey: strings.TrimSpace(values["key"]),
		BaseURL: spec.BaseURL, API: spec.API,
	}
	required := func(key, label string) (string, error) {
		value := strings.TrimSpace(values[key])
		if value == "" {
			return "", fmt.Errorf("%s is required", label)
		}
		return value, nil
	}
	switch spec.ID {
	case "azure-openai-responses":
		base, err := required("base", "Azure base URL")
		if err != nil {
			return credential, err
		}
		credential.BaseURL = NormalizeAzureBaseURL(base)
	case "cloudflare-ai-gateway", "cloudflare-workers-ai":
		account, err := required("account", "Cloudflare account ID")
		if err != nil {
			return credential, err
		}
		credential.Env = map[string]string{"CLOUDFLARE_ACCOUNT_ID": account}
		if spec.ID == "cloudflare-ai-gateway" {
			gateway, gatewayErr := required("gateway", "Cloudflare AI Gateway ID")
			if gatewayErr != nil {
				return credential, gatewayErr
			}
			credential.Env["CLOUDFLARE_GATEWAY_ID"] = gateway
			credential.BaseURL = "https://gateway.ai.cloudflare.com/v1/" + account + "/" + gateway + "/compat"
		} else {
			credential.BaseURL = "https://api.cloudflare.com/client/v4/accounts/" + account + "/ai/v1"
		}
	case "google-vertex":
		project, err := required("project", "Google Cloud project")
		if err != nil {
			return credential, err
		}
		location, err := required("location", "Google Cloud location")
		if err != nil {
			return credential, err
		}
		credential.Env = map[string]string{"GOOGLE_CLOUD_PROJECT": project, "GOOGLE_CLOUD_LOCATION": location}
		credential.ProjectID = project
		credential.BaseURL = "https://" + location + "-aiplatform.googleapis.com/v1/projects/" + project + "/locations/" + location + "/publishers/google"
	}
	return credential, nil
}

func ProviderSlug(value string) string {
	var result strings.Builder
	separator := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(char)
			separator = false
		} else {
			separator = true
		}
	}
	if result.Len() == 0 {
		return "custom"
	}
	return result.String()
}

func MaskedAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return "••••"
	}
	return "••••" + value[len(value)-4:]
}
