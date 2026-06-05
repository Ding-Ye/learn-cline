---
title: "多模型接入指南"
chapter: M
slug: multi-model
est_read_min: 8
---

# 多模型接入指南（DeepSeek / Qwen / 自托管 …）

> 教什么：每个调用 LLM 的章节都走同一个 `Provider` 接口，所以只改一个命令行参数，
> 就能让 mini-cline 跑在 Anthropic、任意 OpenAI 兼容端点，或本地模型上。

## Why multi-model / 为什么要多模型

真正的 cline 对接 40+ 模型后端。我们的 mini 保留同样的思想，但接口面小得多：一个
`Provider` 抽象，两个具体实现——Anthropic 原生，以及一个 OpenAI 兼容客户端。因为每个
调用 LLM 的章节（s01 的 agent loop、s05 的流式 provider）都只依赖这个接口，你可以把
整套课程指向 **DeepSeek、Qwen、Moonshot/Kimi、Groq、OpenRouter，或本地 vLLM/SGLang**，
而不用改任何章节逻辑。

对学习者很实用：Anthropic key 不总是现成的，用 DeepSeek 或本地模型迭代教学仓库便宜得多。

## The Provider abstraction / Provider 抽象

`agents/s01-minimum-agent-loop/provider.go` 定义契约，`provider_openai.go` 给出
OpenAI 兼容实现。s05 把它推广到流式：

- `agents/s05-provider-streaming/provider.go`——流式 `Provider` 接口（一个 `StreamChunk`
  事件通道）加上原生 Anthropic 的 SSE 解码器。
- `agents/s05-provider-streaming/provider_openai.go`——同一接口，跑在 OpenAI 兼容的
  `/chat/completions` SSE 流上。
- `agents/s05-provider-streaming/factory.go`——`buildHandler(name)` 按 profile 名选择 provider。

loop 从不 import 具体 provider；它依赖接口，所以换后端只是改构造函数，不是重写。

## One-line swap / 一行切换

设置对应的 API key，再传 `-provider`（可选 `-model`）：

```bash
# Anthropic（默认）
export ANTHROPIC_API_KEY=sk-ant-...
go run . "build a TODO CLI"

# DeepSeek
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -model deepseek-chat "build a TODO CLI"

# 本地 vLLM / SGLang（OpenAI 兼容）
go run . -provider local -base-url http://localhost:8000/v1 -model my-model "..."
```

| Profile | 环境变量 | 默认 base URL |
|---|---|---|
| anthropic | `ANTHROPIC_API_KEY` | api.anthropic.com（原生） |
| openai | `OPENAI_API_KEY` | api.openai.com/v1 |
| deepseek | `DEEPSEEK_API_KEY` | api.deepseek.com/v1 |
| moonshot | `MOONSHOT_API_KEY` | api.moonshot.cn/v1 |
| qwen | `DASHSCOPE_API_KEY` | dashscope.aliyuncs.com/compatible-mode/v1 |
| groq | `GROQ_API_KEY` | api.groq.com/openai/v1 |
| openrouter | `OPENROUTER_API_KEY` | openrouter.ai/api/v1 |
| local | `OPENAI_API_KEY` | http://localhost:8000/v1 |

## Mapping to upstream cline / 与上游 cline 的对应

上游 cline 用一套大得多的 handler 做同样的事：`apps/vscode/src/core/api/index.ts` 里的
`buildApiHandler` 构造 `apps/vscode/src/core/api/providers/` 下众多 `ApiHandler` 实现之一
（anthropic.ts、openai.ts、gemini、bedrock、ollama……），每个都归一化到
`apps/vscode/src/core/api/transform/` 里的统一 `ApiStream` chunk 形态。我们的 mini 用
工厂 + 统一流的同样模式，只覆盖 Anthropic 原生与 OpenAI 兼容两大家族。

## Caveats / 注意事项

- **流式差异**：各家 SSE 分帧略有不同；我们的 OpenAI 客户端面向通用的
  `/chat/completions` delta 形态。
- **工具/格式支持**：不是每个后端都支持相同的 tool-calling 或 JSON 模式；s02 的
  XML 风格工具协议与 provider 无关，即使后端没有原生 tool-calling 也能用。
- **上下文窗口**差异很大——s08 的截断预算应按模型调整。
- **有些章节根本不调用 LLM**（s07 diff、s08 context、s10 checkpoints 是纯机制 demo）；
  多模型只影响真正对接 provider 的章节。
