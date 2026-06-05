---
title: "Multi-model guide"
chapter: M
slug: multi-model
est_read_min: 8
---

# Multi-model guide (DeepSeek / Qwen / self-hosted …)

> What this teaches: every chapter that calls an LLM goes through one `Provider`
> interface, so you can run the mini-cline against Anthropic, any OpenAI-compatible
> endpoint, or a local model by changing a single flag.

## Why multi-model / 为什么要多模型

The real cline talks to 40+ model backends. Our mini keeps the same idea but a
much smaller surface: a single `Provider` abstraction with two concrete
implementations — native Anthropic and an OpenAI-compatible client. Because every
LLM-calling chapter (s01 the agent loop, s05 the streaming provider) depends only
on that interface, you can point the whole curriculum at **DeepSeek, Qwen,
Moonshot/Kimi, Groq, OpenRouter, or a local vLLM/SGLang server** without touching
any chapter logic.

This matters for learners: Anthropic keys are not always handy, and iterating on a
teaching repo is much cheaper against DeepSeek or a local model.

## The Provider abstraction / Provider 抽象

`agents/s01-minimum-agent-loop/provider.go` defines the contract; `provider_openai.go`
implements it for OpenAI-compatible servers. s05 generalises this to streaming:

- `agents/s05-provider-streaming/provider.go` — the streaming `Provider` interface
  (a channel of `StreamChunk` events) plus the native Anthropic SSE decoder.
- `agents/s05-provider-streaming/provider_openai.go` — the same interface over an
  OpenAI-compatible `/chat/completions` SSE stream.
- `agents/s05-provider-streaming/factory.go` — `buildHandler(name)` selects a
  provider by profile name.

The loop never imports a concrete provider; it depends on the interface, so swapping
backends is a constructor change, not a rewrite.

## One-line swap / 一行切换

Set the matching API key, then pass `-provider` (and optionally `-model`):

```bash
# Anthropic (default)
export ANTHROPIC_API_KEY=sk-ant-...
go run . "build a TODO CLI"

# DeepSeek
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -model deepseek-chat "build a TODO CLI"

# Local vLLM / SGLang (OpenAI-compatible)
go run . -provider local -base-url http://localhost:8000/v1 -model my-model "..."
```

| Profile | Env var | Default base URL |
|---|---|---|
| anthropic | `ANTHROPIC_API_KEY` | api.anthropic.com (native) |
| openai | `OPENAI_API_KEY` | api.openai.com/v1 |
| deepseek | `DEEPSEEK_API_KEY` | api.deepseek.com/v1 |
| moonshot | `MOONSHOT_API_KEY` | api.moonshot.cn/v1 |
| qwen | `DASHSCOPE_API_KEY` | dashscope.aliyuncs.com/compatible-mode/v1 |
| groq | `GROQ_API_KEY` | api.groq.com/openai/v1 |
| openrouter | `OPENROUTER_API_KEY` | openrouter.ai/api/v1 |
| local | `OPENAI_API_KEY` | http://localhost:8000/v1 |

## Mapping to upstream cline / 与上游 cline 的对应

Upstream cline does the same job with a much larger handler set: `buildApiHandler`
in `apps/vscode/src/core/api/index.ts` constructs one of many `ApiHandler`
implementations under `apps/vscode/src/core/api/providers/` (anthropic.ts,
openai.ts, gemini, bedrock, ollama, …), each normalising to a common
`ApiStream` chunk shape in `apps/vscode/src/core/api/transform/`. Our mini mirrors
the factory + common-stream pattern with just the Anthropic-native and
OpenAI-compatible families.

## Caveats / 注意事项

- **Streaming differences**: providers frame SSE slightly differently; our OpenAI
  client targets the common `/chat/completions` delta shape.
- **Tool/format support**: not every backend supports the same tool-calling or
  JSON modes; the XML-style tool protocol (s02) is provider-agnostic and works
  even where native tool-calling does not.
- **Context windows** vary widely — s08's truncation budget should be tuned per
  model.
- **Some chapters don't call an LLM at all** (s07 diff, s08 context, s10
  checkpoints are pure mechanism demos); multi-model only affects the chapters
  that actually talk to a provider.
