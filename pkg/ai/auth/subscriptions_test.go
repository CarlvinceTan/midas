package auth

import (
	"encoding/base64"
	"testing"
)

func TestOpenAIAccountIDFromJWT(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_123"}}`))
	if got := openAIAccountID("header." + payload + ".signature"); got != "acct_123" {
		t.Fatalf("account ID = %q", got)
	}
	if got := openAIAccountID("not-a-jwt"); got != "" {
		t.Fatalf("invalid account ID = %q", got)
	}
}

func TestCopilotProxyEndpointParsing(t *testing.T) {
	token := "tid=abc;exp=1;proxy-ep=proxy.individual.githubcopilot.com;"
	match := copilotProxyEndpoint.FindStringSubmatch(token)
	if len(match) != 2 || match[1] != "proxy.individual.githubcopilot.com" {
		t.Fatalf("match = %#v", match)
	}
}
