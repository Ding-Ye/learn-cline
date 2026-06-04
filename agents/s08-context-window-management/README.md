# s08 · Context Window Management / 上下文窗口管理

> When the conversation outgrows the model's window, drop the OLD middle —
> keep the first task message and the most recent turns.
> 当对话超出模型窗口时，丢掉**中间**的旧消息——保留第一条任务消息和最近几轮。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s08`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 8 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

Earlier chapters let history grow unbounded. But every model has a fixed context
window, and a long coding session blows past it. cline's fix is **truncation**:
estimate the request's token cost and, when it nears the budget, drop a range of
old MIDDLE messages — always keeping the first user/assistant pair (the task) and
the most recent turns (where the model is working). The tricky part is doing this
without corrupting the `tool_use` / `tool_result` pairing the API requires.

前面的章节让历史无限增长。但每个模型的上下文窗口是固定的，长会话很快撑爆。
cline 的做法是**截断**：估算请求的 token 成本，逼近预算时就丢掉一段中间的旧消息——
始终保留第一对 user/assistant（任务定义）和最近几轮（模型正在工作的地方）。
难点在于：丢消息时不能破坏 API 要求的 `tool_use` / `tool_result` 配对。

```
  estimateTokens(System + Messages + Tools)  >  Budget ?
        │ no → send as-is                       │ yes
        ▼                                        ▼
   [unchanged]                          nextTruncationRange(messages)
                                                 │  keep [0,1] ; drop even middle ;
                                                 │  end on an assistant message
                                                 ▼
                                   applyTruncation: [0,1] + tail
                                                 │  strip orphaned tool_results
                                                 ▼
                                   [first pair] [.. recent tail ..]
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | canonical wire types (`Message`, `ContentBlock`, `ToolSchema`, request/response) — the block model truncation operates on |
| `context.go` | `estimateTokens` (char/4 heuristic); `ContextManager` (Budget + KeepRecent); `nextTruncationRange`; `truncate` / `applyTruncation`; orphan-`tool_result` strip |
| `main.go` | offline demo: fabricate a long conversation, show before/after message counts + est tokens, prove the invariants held |
| `context_test.go` | 7 tests, fully offline (no network, no LLM) |

---

## Try it / 动手试一试

Tests need no network and no API key / 测试无需联网、无需 API key：

```bash
cd agents/s08-context-window-management
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Watch truncation with the offline demo / 用离线 demo 观察截断：

```bash
# Default: a 25-message history overflows a 3000-token budget → truncates.
go run .

# Huge budget → nothing to do (returned unchanged).
go run . -budget 100000

# Tight budget → one pass isn't enough; cline would truncate again next request.
go run . -budget 1500

# Keep more recent turns, or fabricate a longer conversation.
go run . -keep 8 -turns 30
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt) — the
demo is deterministic, so it matches byte-for-byte.

预期输出形态见该文件——本 demo 完全确定，逐字一致。

---

## Deliberately omitted / 故意省略

cline's `ContextManager` is ~1300 lines. Before truncating it tries
`attemptFileReadOptimization` — collapsing duplicate file reads to a "[duplicate]"
notice, which can free enough space (>=30%) to skip truncation entirely. It also
persists per-message "context history updates" to disk for checkpointing, and has
an auto-condense path that summarizes old turns via the model. s08 teaches only
the **truncation core**: estimate → range → trim → keep pairing valid. We also use
a char/4 estimator instead of a real tokenizer, so tests assert *relative*
behavior, never exact counts.

cline 的 `ContextManager` 约 1300 行。截断前它会先尝试 `attemptFileReadOptimization`——
把重复的文件读取折叠成 "[duplicate]" 提示，省下的空间够多（>=30%）就能跳过截断。
它还会把每条消息的"上下文历史更新"持久化到磁盘以支持检查点，并有一条用模型总结旧
对话的 auto-condense 路径。s08 只教**截断核心**：估算 → 范围 → 裁剪 → 保持配对有效。
我们用 char/4 估算而非真实分词器，所以测试断言的是*相对*行为，绝不是精确计数。

See `docs/en/s08-context-window-management.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s08-context-window-management.md`。
