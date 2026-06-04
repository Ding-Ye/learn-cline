# s01 · Minimum Agent Loop / 最小智能体循环

> The smallest thing that earns the name "agent": a loop around the LLM call.
> 配得上"智能体"三个字的最小实现：把 LLM 调用包进一个循环。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s01`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 1 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

An LLM call is one-shot: ask once, get one answer. But when the model says
"I want to read `main.go`", a bare call just *stops*. An **agent** is the loop
that runs the requested tool, feeds the result back as the next turn, and asks
again — until the model stops asking for tools. That loop is the entire mechanism
of this chapter (cline calls it `recursivelyMakeClineRequests`).

LLM 调用是一次性的：问一次，答一次。可当模型说"我想读 `main.go`"时，单纯的调用
就到此为止了。**智能体**就是那个循环：执行模型请求的工具，把结果作为下一轮输入喂
回去，再问一次——直到模型不再请求工具。这个循环就是本章的全部机制。

```
user prompt ─▶ Provider.CreateMessage ─▶ assistant turn
                       ▲                        │
                       │                   stop_reason?
              tool_result (user)           /        \
                       │              tool_use      end_turn ─▶ done
                       └──── run tool ◀──┘
```

s01 uses the Anthropic **native `tool_use` block** shape (the simplest path).
s02 will teach cline's custom *streaming XML* assistant-message parser.

s01 用 Anthropic 原生的 `tool_use` 块（最简单的形态）。s02 会讲 cline 自定义的
**流式 XML** 助手消息解析器。

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | `Message` / `ContentBlock` (text/tool_use/tool_result) / `ToolSchema` / `Provider` interface / `AnthropicProvider` |
| `provider_openai.go` | `OpenAIProvider` — translates the Anthropic block model ⇄ OpenAI Chat Completions |
| `tools.go` | `Tool` interface + `write_file` (sandboxed) + `bash` |
| `loop.go` | `Task.Run` — the loop: request → tool → result → repeat, with a turn cap |
| `main.go` | CLI: provider profiles + `-provider` / `-model` / `-base-url` / `-v` flags |
| `loop_test.go`, `provider_openai_test.go` | 9 tests, fully offline (a `fakeProvider`) |

---

## Try it / 动手试一试

Tests need no network and no API key / 测试无需联网、无需 API key：

```bash
cd agents/s01-minimum-agent-loop
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Run the real agent / 跑真实的智能体 (needs a key for your chosen provider):

```bash
# Anthropic (default)
export ANTHROPIC_API_KEY=sk-...
go run . -v "create hello.txt containing the text hi"

# Any OpenAI-compatible provider / 任意 OpenAI 兼容供应商
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -v "run: echo hello from bash"

# Self-hosted / 本地模型 (vLLM, SGLang, ...)
go run . -provider local -model my-model -v "list files in the current dir"
```

Provider profiles: `anthropic | openai | deepseek | moonshot | qwen | groq | openrouter | local`.
Each reads its own API-key env var; see `go run . -h`.

供应商档位见上；每个读取各自的 API-key 环境变量，`go run . -h` 查看详情。

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt).
预期输出形态见该文件——LLM 输出不确定，关注"形态"而非逐字一致。

---

## Deliberately omitted / 故意省略

`task/index.ts` is 3,764 lines. s01 teaches only the loop *shape*
(request → tool → result → loop, plus a turn cap). Streaming (s05),
the XML parser (s02), the tool registry (s03), approval gating (s04),
context truncation (s08), and checkpoints (s10) all come later.

`task/index.ts` 有 3764 行。s01 只教循环的**骨架**。流式、XML 解析、工具注册表、
审批门控、上下文截断、检查点都在后续章节。

See `docs/en/s01-minimum-agent-loop.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s01-minimum-agent-loop.md`。
