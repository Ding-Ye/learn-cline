# s03 · Tool Registry & Execution / 工具注册与执行

> A loop with one hard-coded tool doesn't scale. This chapter builds the
> name-dispatched registry that lets ~27 tools coexist.
> 只有一个写死的工具的循环无法扩展。本章构建按名字分发的注册表，让几十个工具共存。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s03`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 3 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

s01 ran one inline tool; s02 parsed tool calls out of a stream. But a real
agent has *many* tools, and the model picks one **by name** each turn. cline
routes ~27 tools through a coordinator: a `map[string]handler` plus an
`execute(block)` that looks the handler up, validates its params, runs it, and
turns the outcome into a `tool_result` — including for the unhappy paths
(unknown tool, missing param), which must never crash the loop.

s01 跑一个内联工具，s02 从流里解析出工具调用。但真实的智能体有 *很多* 工具，模型
每一轮 **按名字** 挑一个。cline 用一个协调器路由约 27 个工具：一个 `map[string]
handler`，外加一个 `execute(block)`——查找处理器、校验参数、执行、把结果变成
`tool_result`，包括出错路径（未知工具、缺参数），这些都绝不能让循环崩溃。

```
parsed tool_use(block)
        │
        ▼
  registry.Has(name)? ──no──▶ error tool_result ("unknown tool")
        │ yes
        ▼
  required params present? ──no──▶ error tool_result ("missing param")
        │ yes
        ▼
  handler.Execute(params) ──err──▶ error tool_result (err text)
        │ ok
        ▼
   tool_result{ output, call_id }
```

Every path returns a `tool_result` carrying the `tool_use` id, so s01's loop can
append it as the next user turn and keep going.

每条路径都返回带 `tool_use` id 的 `tool_result`，于是 s01 的循环能把它作为下一个
user 轮次追加进去，继续运行。

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | Canonical types: `Message` / `ContentBlock` / `ToolSchema` / `ToolUse` / `ToolResult` |
| `tools.go` | `ToolHandler` interface; `Registry` (register + lookup); 4 sandboxed handlers: `read_file`, `write_to_file`, `list_files`, `execute_command` |
| `executor.go` | `ToolExecutor.Execute(block) -> ToolResult`: dispatch + param validation + unknown-tool/error handling |
| `main.go` | Offline demo: register 4 tools, dispatch a scripted block sequence |
| `tools_test.go`, `executor_test.go` | 11 tests, fully offline (`t.TempDir` sandbox) |

---

## Try it / 动手试一试

No network, no API key — s03 is fully deterministic / 无需联网、无需 API key，
完全确定性：

```bash
cd agents/s03-tool-registry-execution

# the scripted dispatch sequence / 脚本化的分发序列
go run .

# also print each registered tool's schema / 同时打印每个工具的 schema
go run . -v

# tests / 测试
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt) —
for s03 it's byte-stable (the temp sandbox path never appears).

预期输出形态见该文件——s03 是逐字稳定的（临时沙箱路径不会出现在输出里）。

---

## Deliberately omitted / 故意省略

s03 dispatches **every** tool unconditionally — there is no approval gate yet.
That is s04's job (`autoApprove.ts` + `Task.ask`). We also keep just 4 of cline's
~27 handlers, skip `.clineignore` access control, partial-block streaming, and
parallel tool calling. The point here is the *registry + dispatch* shape.

s03 无条件分发 **每个** 工具——还没有审批门控，那是 s04 的活
（`autoApprove.ts` + `Task.ask`）。我们也只保留 cline 约 27 个处理器中的 4 个，
跳过 `.clineignore` 访问控制、部分块流式、并行工具调用。本章的重点是
*注册表 + 分发* 的骨架。

See `docs/en/s03-tool-registry-execution.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s03-tool-registry-execution.md`。
