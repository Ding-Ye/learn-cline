---
title: "s09 · MCP Integration"
chapter: 9
slug: s09-mcp-integration
est_read_min: 11
---

# s09 · MCP Integration

> What this teaches: a minimal MCP (Model Context Protocol) client over JSON-RPC 2.0 — `initialize`, `tools/list`, `tools/call` — that discovers an external server's tools and merges them into the registry as first-class `Tool`s. It gets its own chapter because "extend the agent's capabilities at runtime, from a process you didn't compile" is a structurally different move from s03's compiled-in registry.

---

## Problem

s03 gave the agent a registry of tools and dispatched them by name, but every one of those tools was *compiled into the binary*. That ceiling is real: you can't ship a tool for every database, SaaS API, or internal service a user might have, and you certainly can't recompile cline each time someone wants a new capability.

MCP (Model Context Protocol) is the escape hatch: cline talks to an **external server** over a JSON-RPC transport, asks what tools it offers, and exposes them to the model as if they were built in. The pain this chapter addresses is the wiring — how do you connect to such a server, discover its tools in a vocabulary your registry already understands, and route a tool call to it — without crashing the loop when the server (a thing you don't control) misbehaves?

## Solution

The mental model: an MCP server is just a JSON-RPC 2.0 peer that answers three methods. The client (1) does an `initialize` handshake to agree on a protocol version, (2) calls `tools/list` and converts each returned definition into our canonical `ToolSchema`, and (3) routes each invocation to `tools/call`. The converted schemas drop straight into the s03 registry, so a remote tool is dispatched exactly like a local one.

Three key design decisions:

1. **Put the transport behind an interface.** The client talks to a `Transport`, not a subprocess. In production that's stdio to a child process; in tests it's a FAKE in-process server. The client can't tell — which makes the whole chapter hermetic.
2. **Discovered tools are first-class `Tool`s.** An `mcpTool` adapter wraps each remote definition: `Schema()` returns what `tools/list` gave us, `Execute()` routes to `tools/call`. The registry holds `Tool`s and never knows which are remote.
3. **Distinguish protocol failure from tool failure.** A missing connection or malformed message is a JSON-RPC error (a Go `error`); a tool that ran and failed comes back as a result with `isError:true`. The model gets to read the latter and recover — never a crash.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────┐
│  McpClient                          MCP server (fake/stdio) │
│     │   initialize  ───────────────────▶  agree version     │
│     │   ◀───────────  InitializeResult                      │
│     │   tools/list  ───────────────────▶  enumerate tools   │
│     │   ◀───────────  [ {name, schema} ... ]                │
│     │        │ convert each → ToolSchema                    │
│     │        ▼                                              │
│   Registry.Merge([]Tool)   ← remote tools now first-class   │
│     │   tools/call {name,args} ────────▶  run tool          │
│     │   ◀───────────  { content, isError }                  │
└────────────────────────────────────────────────────────────┘
```

The core of `mcp.go` — `ListTools` issuing the request and converting each remote definition into the registry's vocabulary, plus the adapter that makes a discovered tool dispatchable:

```go
// ListTools issues "tools/list" and converts each remote definition into our
// canonical ToolSchema — the crux of the chapter.
func (c *McpClient) ListTools() ([]ToolSchema, error) {
	if !c.initialized {
		return nil, fmt.Errorf("ListTools: client not initialized (call Initialize first)")
	}
	var res listToolsResult
	if err := c.call("tools/list", struct{}{}, &res); err != nil {
		return nil, err
	}
	out := make([]ToolSchema, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

// mcpTool adapts one discovered MCP tool into the registry's Tool interface, so a
// server-provided tool is dispatched the same way as a compiled-in one.
type mcpTool struct {
	client *McpClient
	schema ToolSchema
}

func (t *mcpTool) Schema() ToolSchema { return t.schema }

func (t *mcpTool) Execute(args map[string]interface{}) (string, error) {
	res, err := t.client.CallTool(t.schema.Name, args)
	if err != nil {
		return "", err // protocol/transport failure
	}
	if res.IsError {
		return "", fmt.Errorf("%s", res.Text()) // tool-level failure → error result
	}
	return res.Text(), nil
}
```

**Four non-obvious points**:

1. **`initialize` must come first.** `ListTools`/`CallTool` refuse to run on an un-initialized client. A real server tracks lifecycle state and may reject tool traffic before the handshake — cline runs it inside `connectToServer` before any `fetchToolsList`.
2. **`inputSchema` is nearly a field copy.** A remote tool already describes itself with a JSON-Schema object, the same idea as `ToolSchema.InputSchema`. Discovery is conversion, not translation — which is *why* MCP tools merge so cleanly.
3. **Two error channels.** `c.call` returns a `*RPCError` for protocol problems (unknown method `-32601`, parse error `-32700`); a failed tool returns a result with `IsError:true`. The adapter funnels the latter into a Go error so the executor wraps it as an error `tool_result` (the s03 non-crashing discipline).
4. **The transport seam is what makes tests hermetic.** Because the client only sees `Transport.RoundTrip([]byte) []byte`, the fake server *is* the transport — no subprocess, no pipes. The same seam is where a real stdio client would marshal to a child's stdin.

## What Changed (vs. s03)

```diff
  type Registry struct {
  	tools map[string]Tool
  }

+ // Merge folds a batch of tools (e.g. everything an MCP server exposed) into the
+ // registry — the moment a remote server's capabilities become part of the agent.
+ func (r *Registry) Merge(tools []Tool) {
+ 	for _, t := range tools {
+ 		r.Register(t)
+ 	}
+ }

+ // mcpTool: a Tool whose Execute() is a remote tools/call, not local code.
+ type mcpTool struct {
+ 	client *McpClient
+ 	schema ToolSchema
+ }
+ func (t *mcpTool) Schema() ToolSchema { return t.schema }
+ func (t *mcpTool) Execute(args map[string]interface{}) (string, error) { /* → tools/call */ }
```

In s03 the registry was populated once, at startup, with tools whose `Execute` ran in-process. The semantic change in s09 is that the registry is **extended at runtime** with tools whose `Execute` is a network call to a process you didn't write. The model sees no difference; the registry sees no difference. Only the adapter knows there's a JSON-RPC server on the other end.

## Try It

```bash
# No network, no subprocess, no API key — fully deterministic.
cd agents/s09-mcp-integration

# connect to the in-process fake server, discover + merge + call
go run .

# also print the initialize handshake to stderr
go run . -v

# tests
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape:

```
discovered 2 MCP tool(s):
  - add    Add two integers a and b.
  - echo   Echo back the provided text.

calling tools through the registry:
  echo   -> ok    hello from MCP
  add    -> ok    42
  echo   -> ERROR echo requires a non-empty 'text' argument
```

The two tools are *discovered from the server*, not compiled in; the final ERROR is a tool-level failure surfaced as a result (not a crash). With `-v`, `[s09] handshake ok: server=fake-mcp-server ...` prints to stderr first.

## Upstream Source Reading

cline's MCP integration lives in `McpHub.ts`. `connectToServer` (L286) opens a transport and connects the SDK `Client` (whose `connect()` runs the `initialize` handshake), then fetches the server's tools; `fetchToolsList` (L677) issues `tools/list`; `callTool` (L1233) issues `tools/call`. The big difference: cline delegates the JSON-RPC wire and the stdio/SSE transports to `@modelcontextprotocol/sdk`, whereas s09 implements the protocol by hand and routes it through a `Transport` interface so a fake server can stand in.

```upstream:apps/vscode/src/services/mcp/McpHub.ts#L677-L711
private async fetchToolsList(serverName: string): Promise<McpTool[]> {
	try {
		const connection = this.connections.find((conn) => conn.server.name === serverName)
		if (!connection) {
			throw new Error(`No connection found for server: ${serverName}`)
		}
		// Disabled servers don't have clients, so return empty tools list
		if (connection.server.disabled || !connection.client) {
			return []
		}

		// THE request: a typed JSON-RPC call. method "tools/list", validated
		// against ListToolsResultSchema, with a timeout. s09's ListTools is this.
		const response = await connection.client.request({ method: "tools/list" }, ListToolsResultSchema, {
			timeout: DEFAULT_REQUEST_TIMEOUT_MS,
		})

		// Get autoApprove settings — the seam to the approval gate (s04).
		const settingsPath = await getMcpSettingsFilePathHelper(await this.getSettingsDirectoryPath())
		const content = await fs.readFile(settingsPath, "utf-8")
		const config = JSON.parse(content)
		const autoApproveConfig = config.mcpServers[serverName]?.autoApprove || []

		// Mark tools as always allowed based on settings (s09 omits this flag).
		const tools = (response?.tools || []).map((tool) => ({
			...tool,
			autoApprove: autoApproveConfig.includes(tool.name),
		}))

		return tools
	} catch (error) {
		// A flaky server returns [] rather than throwing — it must not take down
		// the whole task. (s09 surfaces the error instead, for teaching clarity.)
		Logger.error(`Failed to fetch tools for ${serverName}:`, error)
		return []
	}
}
```

**Reading notes**:

- **Discovery = `request("tools/list")` + a map step**: upstream maps each tool to add an `autoApprove` flag; s09 maps each to a `ToolSchema`. Both are "convert the server's tool list into our vocabulary".
- **`autoApprove` we don't have**: upstream tags each tool with an approval flag here, wiring discovery into s04's gate. s09 has no approval yet, so the field is dropped.
- **Async SDK vs. sync hand-rolled wire**: upstream's `connection.client.request(...)` is the SDK's typed, schema-validated, timed JSON-RPC call; s09's `c.call(method, params, &result)` is the same idea written out by hand over a `Transport`.
- **Failure handling differs deliberately**: upstream swallows errors and returns `[]` so one bad server can't kill the task; s09 returns the error so a learner sees exactly what broke.
- **Resources & prompts elided**: `connectToServer` also calls `fetchResourcesList`/`fetchPromptsList` (L658-660); s09 keeps only `tools/*` — correct-but-partial, on purpose.

**Read further**: start at `McpHub.ts` → `connectToServer` (L286), follow it to the `fetchToolsList` call (L657) and into `fetchToolsList` (L677), then `callTool` (L1233). From there the discovered tools surface via `UseMcpToolHandler`, which routes back into `McpHub.callTool` — a registry entry whose `Execute()` is a remote call. That trace is the real-source map for s09 → s04 (auto-approve) → s06 (tools in the prompt).

---

**Next**: s10 adds shadow-git checkpoints so every edit the agent makes — including those driven by MCP tools — can be snapshotted and undone.
