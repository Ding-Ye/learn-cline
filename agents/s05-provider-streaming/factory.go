package main

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// buildHandler — the provider factory.
//
// Mirrors apps/vscode/src/core/api/index.ts buildApiHandler / the
// createHandlerForProvider switch (L76). cline maps a provider id string +
// config to a concrete ApiHandler; we do the same, returning a Provider so the
// agent loop is written ONCE against the interface and the choice of vendor is
// a single string the caller picks at startup.
// ---------------------------------------------------------------------------

// ProviderConfig is the minimal config a provider needs (cline threads a much
// larger ApiConfiguration through; we keep just the load-bearing fields).
type ProviderConfig struct {
	APIKey  string
	Model   string
	BaseURL string // OpenAI-compatible providers only
}

// buildHandler selects a Provider by name. The OpenAI-compatible aliases
// (deepseek, moonshot, qwen, groq, openrouter, local, ...) all resolve to the
// same OpenAIProvider with a different default base URL — exactly how cline
// reuses one handler across many compatible vendors.
func buildHandler(name string, cfg ProviderConfig) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "anthropic", "":
		model := cfg.Model
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
		return NewAnthropicProvider(cfg.APIKey, model), nil

	case "openai":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "https://api.openai.com/v1"), cfg.Model), nil
	case "deepseek":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "https://api.deepseek.com/v1"), cfg.Model), nil
	case "moonshot":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "https://api.moonshot.cn/v1"), cfg.Model), nil
	case "qwen":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "https://dashscope.aliyuncs.com/compatible-mode/v1"), cfg.Model), nil
	case "groq":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "https://api.groq.com/openai/v1"), cfg.Model), nil
	case "openrouter":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "https://openrouter.ai/api/v1"), cfg.Model), nil
	case "local":
		return NewOpenAIProvider(cfg.APIKey, orDefault(cfg.BaseURL, "http://localhost:8000/v1"), cfg.Model), nil

	default:
		// cline's anti-pattern is silently resetting an unknown provider to
		// Anthropic; we error loudly instead (research-notes "Anti-Patterns").
		return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, deepseek, moonshot, qwen, groq, openrouter, local)", name)
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
