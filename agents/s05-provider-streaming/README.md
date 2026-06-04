# s05 · Provider Streaming Abstraction / 供应商流式抽象

> One async stream of chunks, many providers, picked by a factory.
> 一条异步的 chunk 流，多个供应商，由工厂选择。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s05`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 5 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

s01-s04 used a fake, buffered provider. A real agent must **stream** Server-Sent
Events from an actual API and normalize each vendor's chunks into ONE internal
event type, behind a **factory** so providers are swappable. cline does this for
~40 providers; we do it for two: a native **AnthropicProvider** (SSE
`message_start` / `content_block_delta` / `message_stop`) and an
**OpenAI-compatible** provider (SSE `/chat/completions` deltas). Both emit the
same `StreamChunk` — the agent loop never learns which one answered.

s01-s04 用的是假的、缓冲式的 provider。真实的智能体必须从真实 API **流式**读取
Server-Sent Events，并把各家供应商的 chunk 归一成同一种内部事件类型，再用一个
**工厂**让供应商可替换。cline 对约 40 个供应商这么做；我们做两个：原生的
**AnthropicProvider** 与 **OpenAI 兼容** provider。两者吐出同一种 `StreamChunk`。

```
                      ┌──────────────── buildHandler(name) ───────────────┐
                      ▼                                                    ▼
  HTTP POST stream:true                                       HTTP POST stream:true
  api.anthropic.com/v1/messages                          .../v1/chat/completions
        │ SSE: message_start / content_block_delta            │ SSE: choices[].delta
        ▼                                                      ▼
  decodeAnthropicSSE                                     decodeOpenAISSE
        └──────────────┐                       ┌───────────────┘
                       ▼                       ▼
              <-chan StreamChunk  (text | tool_use_start | tool_use_delta | usage | done)
                       │
                       ▼
        assembleResponse  →  one CreateMessageResponse  (text + tool_use, usage)
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | `StreamChunk`; streaming `Provider` interface (`CreateMessageStream` + buffered `CreateMessage` fallback); `AnthropicProvider` + its SSE decoder; the shared SSE line reader |
| `provider_openai.go` | `OpenAIProvider` — OpenAI-compatible `/chat/completions` streaming + Anthropic⇄OpenAI request translation |
| `factory.go` | `buildHandler(name, cfg)` — selects a provider by name (errors on unknown, no silent fallback) |
| `main.go` | offline demo (replays a recorded SSE fixture, no key) + `-live` real stream with provider profiles |
| `provider_test.go` | 9 tests, fully offline (fake `strings.Reader` + `httptest`) |

---

## Try it / 动手试一试

Tests need no network and no API key / 测试无需联网、无需 API key：

```bash
cd agents/s05-provider-streaming
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Watch the chunk pipeline with the offline demo / 用离线 demo 观察 chunk 管线
(replays a recorded Anthropic SSE stream, no key needed):

```bash
go run .
```

Stream from a real provider / 从真实供应商流式读取 (needs a key):

```bash
# Anthropic (default)
export ANTHROPIC_API_KEY=sk-...
go run . -live -model claude-sonnet-4-20250514 -prompt "say hi in one line"

# Any OpenAI-compatible provider / 任意 OpenAI 兼容供应商
export DEEPSEEK_API_KEY=sk-...
go run . -live -provider deepseek -model deepseek-chat -prompt "say hi"
```

Provider profiles: `anthropic | openai | deepseek | moonshot | qwen | groq | openrouter | local`.
Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt).

供应商档位见上；预期输出形态见该文件——LLM 输出不确定，关注"形态"而非逐字一致。

---

## Deliberately omitted / 故意省略

cline's anthropic.ts also handles extended thinking, adaptive reasoning, prompt
caching, the 1M-context beta, fast mode, and retries. s05 teaches only the
streaming **shape**: SSE → normalized chunks → factory. Reasoning chunks,
caching token fields, and retry/backoff are left out on purpose.

cline 的 anthropic.ts 还处理扩展思考、自适应推理、prompt 缓存、1M 上下文 beta、
fast mode、重试等。s05 只教流式的**骨架**：SSE → 归一化 chunk → 工厂。

See `docs/en/s05-provider-streaming.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s05-provider-streaming.md`。
