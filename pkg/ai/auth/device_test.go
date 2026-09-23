package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginDevicePollsPendingAndReturnsCredential(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/device":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"device_code": "device", "user_code": "ABCD", "verification_uri": "https://example.com/device", "expires_in": 60,
			})
		case "/token":
			if polls.Add(1) == 1 {
				response.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(response).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	provider := DeviceProvider{ID: "test", Name: "Test", ClientID: "client", DeviceURL: server.URL + "/device", TokenURL: server.URL + "/token", DefaultInterval: time.Millisecond}
	var notified DeviceCode
	credential, err := LoginDevice(context.Background(), provider, server.Client(), func(code DeviceCode) error {
		notified = code
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if notified.UserCode != "ABCD" || credential.AccessToken != "access" || credential.RefreshToken != "refresh" || credential.Flow != "test" {
		t.Fatalf("notification = %#v, credential = %#v", notified, credential)
	}
	if polls.Load() != 2 {
		t.Fatalf("polls = %d", polls.Load())
	}
}

func TestDeviceProviderCatalogMatchesSupportedSubscriptions(t *testing.T) {
	for _, id := range []string{"xai", "kimi-coding", "meta"} {
		provider, ok := DeviceProviderByID(id)
		if !ok || provider.ClientID == "" || provider.DeviceURL == "" || provider.TokenURL == "" {
			t.Fatalf("device provider %q = %#v, %v", id, provider, ok)
		}
	}
}
