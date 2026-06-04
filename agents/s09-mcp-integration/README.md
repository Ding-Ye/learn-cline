# s09 · MCP Integration / MCP 外部工具集成

> A fixed tool set is limiting. This chapter connects to an external MCP server
> over JSON-RPC 2.0, discovers its tools, and merges them into the registry so
> they dispatch like any built-in tool.
> 固定的工具集是受限的。本章通过 JSON-RPC 2.0 连接外部 MCP 服务器，发现它的工具，
> 并把它们合并进注册表，让它们像内置工具一样被分发。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s09`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 9 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

s03 dispatched ~27 *compiled-in* tools by name. MCP (Model Context Protocol) lets
cline load tools from an **external server** at runtime: it opens a JSON-RPC 2.0
transport, performs an `initialize` handshake, asks `tools/list` for the server's
tools, and routes each invocation to `tools/call`. The discovered tools become
ordinary entries in the registry — the model can't tell them apart from built-ins.
s09 builds that client and runs it against a FAKE in-process server, so everything
is hermetic (no subprocess, no network).

s03 按名字分发约 27 个 *编译进来的* 工具。MCP（模型上下文协议）让 cline 在运行时从
**外部服务器** 加载工具：打开 JSON-RPC 2.0 传输、做 `initialize` 握手、用
`tools/list` 询问服务器有哪些工具、把每次调用路由到 `tools/call`。发现的工具变成
注册表里普通的条目——模型无法把它们和内置工具区分开。s09 构建这个客户端，并对一个
**假的** 进程内服务器运行它，所以一切都是封闭可复现的（无子进程、无网络）。

```
McpClient ──initialize──▶ MCP server   (handshake: agree protocol version)
McpClient ──tools/list──▶ MCP server   (server returns its tool definitions)
   tools/list result ──▶ []ToolSchema ──▶ Registry.Merge   (now first-class tools)
McpClient ──tools/call──▶ MCP server   (invoke one tool, get { content, isError })
```

Every call is one JSON-RPC 2.0 round-trip over a `Transport` interface; the fake
server implements that interface in-process, so the client can't tell it from a
real subprocess.

每次调用都是经过 `Transport` 接口的一次 JSON-RPC 2.0 往返；假服务器在进程内实现该
接口，所以客户端无法把它和真实子进程区分开。

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | Canonical types: `ToolSchema` / `ContentBlock` / `Tool` + `Registry` with `Merge` (fold discovered tools in) |
| `mcp.go` | JSON-RPC 2.0 `Request` / `Response` / `RPCError`; `Transport` interface; `McpClient` with `Initialize`, `ListTools` -> `[]ToolSchema`, `CallTool`; `mcpTool` adapter + `DiscoverTools` |
| `fakeserver.go` | `FakeServer`: an in-process MCP server (implements `Transport`) serving `initialize` / `tools/list` / `tools/call`, with two demo tools (`echo`, `add`) |
| `main.go` | Offline demo: connect to the fake server, discover + merge tools, call them through the registry |
| `mcp_test.go` | 8 tests, fully offline: handshake, list→ToolSchema, call round-trip, tool-level error, unknown method, malformed request, registry merge |

---

## Try it / 动手试一试

No network, no subprocess, no API key — s09 is fully deterministic / 无需联网、
无需子进程、无需 API key，完全确定性：

```bash
cd agents/s09-mcp-integration

# connect to the fake server, discover + merge + call / 连接假服务器，发现+合并+调用
go run .

# also print the initialize handshake to stderr / 同时把握手信息打印到 stderr
go run . -v

# tests / 测试
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt) —
for s09 it's byte-stable (no process pids or paths appear).

预期输出形态见该文件——s09 是逐字稳定的（没有进程 pid 或路径出现在输出里）。

---

## Deliberately omitted / 故意省略

The real `McpHub` is ~1,500 LOC. s09 implements the in-process JSON-RPC transport
only — no real stdio subprocess spawning, no SSE / streamable-HTTP transports, no
OAuth, no server lifecycle (reconnection, file-watching config), no resources or
prompts (only `tools/*`), and no auto-approve tagging (that's s04). The MCP tool
call also bypasses the approval gate here; in the full agent it would pass through
s04 like any destructive tool. The point of this chapter is the *minimal honest
protocol* — handshake, list, call — and the merge into the registry.

真实的 `McpHub` 约 1500 行。s09 只实现进程内 JSON-RPC 传输——没有真实 stdio 子进程
启动、没有 SSE / streamable-HTTP 传输、没有 OAuth、没有服务器生命周期（重连、配置
文件监听）、没有 resources 或 prompts（只有 `tools/*`）、没有 auto-approve 标记
（那是 s04）。这里的 MCP 工具调用也绕过了审批门控；在完整智能体里它会像任何破坏性
工具一样经过 s04。本章的重点是 *最小而诚实的协议*——握手、列举、调用——以及合并进
注册表。

See `docs/en/s09-mcp-integration.md` (and `docs/zh/...`) for the full six-section
walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s09-mcp-integration.md`。
