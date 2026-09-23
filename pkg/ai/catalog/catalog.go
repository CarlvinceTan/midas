// Package catalog describes the provider connections supported by Midas's
// native Go transports. Provider implementations remain in ai/providers; this
// package holds only provider metadata plus the small rules that turn that
// metadata into per-model decisions (see ModelProtocol and ModelSupportsReasoning).
package catalog

import (
	"cmp"
	"slices"
	"strings"
)

type Field struct {
	Key         string
	Label       string
	Placeholder string
	Secret      bool
}

type Provider struct {
	ID          string
	Name        string
	Description string
	API         string
	BaseURL     string
	EnvVars     []string
	Fields      []Field
	// MixedProtocols marks a provider that answers more than one protocol from
	// a single base URL, so its models cannot share one API. Discovered models
	// take their protocol from the endpoints they advertise; see ModelProtocol.
	MixedProtocols bool
}

var apiKeyProviders = []Provider{
	{ID: "anthropic", Name: "Anthropic", Description: "Claude API", API: "anthropic-messages", EnvVars: []string{"ANTHROPIC_API_KEY"}},
	{ID: "ant-ling", Name: "Ant Ling", Description: "OpenAI-compatible API", API: "openai-completions", BaseURL: "https://api.ant-ling.com/v1", EnvVars: []string{"ANT_LING_API_KEY"}},
	{ID: "azure-openai-responses", Name: "Azure OpenAI", Description: "Azure Responses API", API: "openai-responses", EnvVars: []string{"AZURE_OPENAI_API_KEY"}, Fields: []Field{{Key: "base", Label: "Azure base URL", Placeholder: "https://resource.openai.azure.com/openai/v1"}}},
	{ID: "baseten", Name: "Baseten", Description: "Baseten inference", API: "openai-completions", BaseURL: "https://inference.baseten.co/v1", EnvVars: []string{"BASETEN_API_KEY"}},
	{ID: "cerebras", Name: "Cerebras", Description: "Cerebras inference", API: "openai-completions", BaseURL: "https://api.cerebras.ai/v1", EnvVars: []string{"CEREBRAS_API_KEY"}},
	{ID: "cloudflare-ai-gateway", Name: "Cloudflare AI Gateway", Description: "Cloudflare gateway billing or BYOK", API: "openai-completions", EnvVars: []string{"CLOUDFLARE_API_KEY"}, Fields: []Field{{Key: "account", Label: "Cloudflare account ID", Placeholder: "Account ID"}, {Key: "gateway", Label: "AI Gateway ID", Placeholder: "Gateway slug"}}},
	{ID: "cloudflare-workers-ai", Name: "Cloudflare Workers AI", Description: "Workers AI OpenAI compatibility API", API: "openai-completions", EnvVars: []string{"CLOUDFLARE_API_KEY"}, Fields: []Field{{Key: "account", Label: "Cloudflare account ID", Placeholder: "Account ID"}}},
	{ID: "command-code", Name: "CommandCode", Description: "CommandCode plan or Provider API credits", API: "openai-completions", BaseURL: "https://api.commandcode.ai/provider/v1", EnvVars: []string{"CMD_API_KEY", "COMMAND_CODE_API_KEY"}, MixedProtocols: true},
	{ID: "deepseek", Name: "DeepSeek", Description: "DeepSeek API", API: "openai-completions", BaseURL: "https://api.deepseek.com", EnvVars: []string{"DEEPSEEK_API_KEY"}},
	{ID: "fireworks", Name: "Fireworks", Description: "Fireworks inference", API: "openai-completions", BaseURL: "https://api.fireworks.ai/inference/v1", EnvVars: []string{"FIREWORKS_API_KEY"}},
	{ID: "google", Name: "Google Gemini", Description: "Gemini Developer API", API: "google-generative-ai", EnvVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	{ID: "google-vertex", Name: "Google Vertex AI", Description: "Vertex AI with a Google Cloud API key", API: "google-generative-ai", EnvVars: []string{"GOOGLE_CLOUD_API_KEY"}, Fields: []Field{{Key: "project", Label: "Google Cloud project", Placeholder: "Project ID"}, {Key: "location", Label: "Google Cloud location", Placeholder: "us-central1"}}},
	{ID: "groq", Name: "Groq", Description: "Groq inference", API: "openai-completions", BaseURL: "https://api.groq.com/openai/v1", EnvVars: []string{"GROQ_API_KEY"}},
	{ID: "huggingface", Name: "Hugging Face", Description: "Hugging Face inference router", API: "openai-completions", BaseURL: "https://router.huggingface.co/v1", EnvVars: []string{"HF_TOKEN"}},
	{ID: "kimi-coding", Name: "Kimi For Coding", Description: "Kimi coding API", API: "anthropic-messages", BaseURL: "https://api.kimi.com/coding", EnvVars: []string{"KIMI_API_KEY"}},
	{ID: "meta", Name: "Meta", Description: "Meta Model API", API: "openai-responses", BaseURL: "https://api.meta.ai/v1", EnvVars: []string{"META_API_KEY"}},
	{ID: "minimax", Name: "MiniMax", Description: "MiniMax global", API: "anthropic-messages", BaseURL: "https://api.minimax.io/anthropic", EnvVars: []string{"MINIMAX_API_KEY"}},
	{ID: "minimax-cn", Name: "MiniMax CN", Description: "MiniMax China", API: "anthropic-messages", BaseURL: "https://api.minimaxi.com/anthropic", EnvVars: []string{"MINIMAX_CN_API_KEY"}},
	{ID: "mistral", Name: "Mistral", Description: "Mistral API", API: "openai-completions", BaseURL: "https://api.mistral.ai/v1", EnvVars: []string{"MISTRAL_API_KEY"}},
	{ID: "moonshotai", Name: "Moonshot AI", Description: "Moonshot global", API: "openai-completions", BaseURL: "https://api.moonshot.ai/v1", EnvVars: []string{"MOONSHOT_API_KEY"}},
	{ID: "moonshotai-cn", Name: "Moonshot AI CN", Description: "Moonshot China", API: "openai-completions", BaseURL: "https://api.moonshot.cn/v1", EnvVars: []string{"MOONSHOT_API_KEY"}},
	{ID: "nvidia", Name: "NVIDIA NIM", Description: "NVIDIA hosted inference", API: "openai-completions", BaseURL: "https://integrate.api.nvidia.com/v1", EnvVars: []string{"NVIDIA_API_KEY"}},
	{ID: "openai", Name: "OpenAI", Description: "OpenAI Platform API", API: "openai-responses", EnvVars: []string{"OPENAI_API_KEY"}},
	{ID: "openrouter", Name: "OpenRouter", Description: "OpenRouter credits or BYOK", API: "openai-completions", BaseURL: "https://openrouter.ai/api/v1", EnvVars: []string{"OPENROUTER_API_KEY"}},
	{ID: "qwen-token-plan", Name: "Qwen Token Plan", Description: "Qwen international token plan", API: "openai-completions", BaseURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", EnvVars: []string{"QWEN_TOKEN_PLAN_API_KEY"}},
	{ID: "qwen-token-plan-individual", Name: "Qwen Token Plan Individual", Description: "Qwen individual subscription", API: "openai-completions", BaseURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", EnvVars: []string{"QWEN_TOKEN_PLAN_API_KEY"}},
	{ID: "qwen-token-plan-cn", Name: "Qwen Token Plan CN", Description: "Qwen China token plan", API: "openai-completions", BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", EnvVars: []string{"QWEN_TOKEN_PLAN_CN_API_KEY"}},
	{ID: "together", Name: "Together AI", Description: "Together inference", API: "openai-completions", BaseURL: "https://api.together.ai/v1", EnvVars: []string{"TOGETHER_API_KEY"}},
	{ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", Description: "Vercel AI Gateway", API: "anthropic-messages", BaseURL: "https://ai-gateway.vercel.sh/v1", EnvVars: []string{"AI_GATEWAY_API_KEY"}},
	{ID: "xai", Name: "xAI", Description: "Grok API", API: "openai-responses", BaseURL: "https://api.x.ai/v1", EnvVars: []string{"XAI_API_KEY"}},
	{ID: "xiaomi", Name: "Xiaomi MiMo", Description: "Xiaomi MiMo API", API: "openai-completions", BaseURL: "https://api.xiaomimimo.com/v1", EnvVars: []string{"XIAOMI_API_KEY"}},
	{ID: "xiaomi-token-plan-cn", Name: "Xiaomi Token Plan CN", Description: "Xiaomi China token plan", API: "openai-completions", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", EnvVars: []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"}},
	{ID: "xiaomi-token-plan-ams", Name: "Xiaomi Token Plan AMS", Description: "Xiaomi Amsterdam token plan", API: "openai-completions", BaseURL: "https://token-plan-ams.xiaomimimo.com/v1", EnvVars: []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"}},
	{ID: "xiaomi-token-plan-sgp", Name: "Xiaomi Token Plan SGP", Description: "Xiaomi Singapore token plan", API: "openai-completions", BaseURL: "https://token-plan-sgp.xiaomimimo.com/v1", EnvVars: []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"}},
	{ID: "zai", Name: "Z.AI", Description: "Z.AI Coding Plan global", API: "openai-completions", BaseURL: "https://api.z.ai/api/coding/paas/v4", EnvVars: []string{"ZAI_API_KEY"}},
	{ID: "zai-coding-cn", Name: "Z.AI Coding CN", Description: "Z.AI Coding Plan China", API: "openai-completions", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", EnvVars: []string{"ZAI_CODING_CN_API_KEY"}},
}

func APIKeyProviders() []Provider {
	result := append([]Provider(nil), apiKeyProviders...)
	slices.SortStableFunc(result, func(a, b Provider) int { return cmp.Compare(a.Name, b.Name) })
	return result
}

func Lookup(id string) (Provider, bool) {
	for _, provider := range apiKeyProviders {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

// ModelProtocol resolves the protocol a model of a MixedProtocols provider
// speaks. The endpoints the provider advertises for that model win, because a
// single model can be reachable through several routes. Chat Completions is
// preferred over Responses when both are advertised, since it is the common
// route for non-Claude models. Without any advertised endpoint the model name
// decides: Claude models go to the Anthropic route, everything else to the
// OpenAI-compatible one.
func ModelProtocol(modelID string, endpoints []string) string {
	var hasMessages, hasCompletions, hasResponses bool
	for _, endpoint := range endpoints {
		switch strings.TrimRight(strings.ToLower(strings.TrimSpace(endpoint)), "/") {
		case "/messages", "messages":
			hasMessages = true
		case "/chat/completions", "chat/completions":
			hasCompletions = true
		case "/responses", "responses":
			hasResponses = true
		}
	}
	switch {
	case hasCompletions:
		return "openai-completions"
	case hasResponses:
		return "openai-responses"
	case hasMessages:
		return "anthropic-messages"
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(modelID)), "claude-"):
		return "anthropic-messages"
	default:
		return "openai-completions"
	}
}

// ModelSupportsReasoning reports whether a model served through a
// MixedProtocols provider exposes reasoning. Anthropic-shaped routes always
// carry thinking; on OpenAI-shaped routes only the families that are known to
// reason without an OpenAI reasoning model name are included.
func ModelSupportsReasoning(modelID, api string) bool {
	if api == "anthropic-messages" {
		return true
	}
	id := strings.ToLower(modelID)
	for _, family := range []string{"deepseek", "kimi", "glm", "qwen", "minimax"} {
		if strings.Contains(id, family) {
			return true
		}
	}
	return false
}
