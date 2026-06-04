---
title: "s03 · Tool Registry & Execution"
chapter: 3
slug: s03-tool-registry-execution
est_read_min: 10
---

# s03 · Tool Registry & Execution

> What this teaches: the tool registry and dispatcher — a `name → handler` map plus an `Execute(block)` that looks the handler up, validates its params, runs it, and turns the outcome (success or failure) into a `tool_result`. It gets its own chapter because "an agent has many tools and the model picks one by name" is the entire basis on which tool capability scales.

---

## Problem

s01's loop had one hard-coded tool; s02 learned to parse tool calls out of a stream. Both chapters dodged a fact no real agent can: there is more than one tool. cline has ~27 of them — `read_file`, `write_to_file`, `execute_command`, `list_files`, `search_files`, MCP calls — and the model picks one **by name** every turn. If dispatch were a long `if name == "read_file" { ... } else if name == "write_to_file" { ... }`, every new tool would mean editing the loop, and the loop would drown in branches.

The subtler issue is the failure paths. Models hallucinate tool names that don't exist, omit required parameters, and tools themselves fail (file missing, command exits non-zero). None of these may **crash** the agent loop — the model has to *see* the error so it can correct itself next turn. So we need more than a lookup table: we need a dispatcher that converts any failure into a `tool_result` the model can read. That registry-plus-dispatcher is what this chapter builds.

## Solution

Mental model: **tool execution moves from "inline" to "name-dispatched through a registry".** The registry is a `map[string]ToolHandler`; the executor takes a parsed `tool_use` block, looks it up, validates, runs it, wraps the result — and that's it.

The load-bearing design is that **every exit of the dispatcher returns a `tool_result`** and never panics. Unknown tool? Error result. Missing param? Error result. Handler errored? Error result. Success? Result with output. The loop can therefore blindly append the result as the next user turn.

Three decisions worth calling out:

1. **The registry *is* the map.** This is exactly cline's `ToolExecutorCoordinator` — a `Map<string, IToolHandler>` with `register` / `has` / `getHandler`. Adding a tool = inserting a map entry; the loop never changes.
2. **Validation precedes execution.** Required params are checked *before* `Execute` is called (cline's `ToolValidator.assertRequiredParams`). A `write_to_file` missing `content` is rejected before it ever touches the disk.
3. **Errors are results, not exceptions.** cline catches thrown errors in `handleError` and `pushToolResult`s an error result. We return an `IsError=true` `ToolResult` directly. The model sees the error; the loop continues.

## How It Works

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                                                              │
│   parsed tool_use(block)                                     │
│        │                                                     │
│        ▼                                                     │
│   registry.Has(name)? ──no──▶ tool_result{ "unknown tool" } │
│        │ yes                                                 │
│        ▼                                                     │
│   required params? ─────no──▶ tool_result{ "missing param" }│
│        │ yes                                                 │
│        ▼                                                     │
│   handler.Execute ──────err─▶ tool_result{ err text }       │
│        │ ok                                                  │
│        ▼                                                     │
│   tool_result{ output, call_id }                            │
│                                                              │
│   (every exit carries call_id; the loop feeds it back as a  │
│    user turn)                                                │
└──────────────────────────────────────────────────────────────┘
```

The load-bearing ~40 lines (excerpt from [`agents/s03-tool-registry-execution/executor.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s03-tool-registry-execution/executor.go)):

```go
// Execute dispatches one parsed tool_use block and returns a ToolResult that is
// always safe to append to the conversation.
func (e *ToolExecutor) Execute(ctx context.Context, block ToolUse) ToolResult {
	// 1. Unknown tool: do not panic, hand the model an error it can recover from.
	handler, ok := e.registry.Get(block.Name)
	if !ok {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error: unknown tool %q. Available tools: %s", block.Name, e.toolNames()),
			IsError: true,
		}
	}

	// 2. Validate required params before doing any work.
	params := flatten(block)
	if missing := assertRequiredParams(handler, params); missing != "" {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error: missing required parameter %q for tool %q.", missing, block.Name),
			IsError: true,
		}
	}

	// 3. Run the tool. A returned error is reported, not thrown.
	out, err := handler.Execute(ctx, params)
	if err != nil {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error executing %s: %v", block.Name, err),
			IsError: true,
		}
	}

	// 4. Success.
	return ToolResult{CallID: block.CallID, Output: out}
}
```

**4 non-obvious points**:

1. **The `Has` check is mandatory.** Look up before you dispatch. This is what keeps a hallucinated tool name from dispatching into nothing, converting it into an ordinary error result — cline's `execute` opens the same way with `if (!this.coordinator.has(block.name))`.
2. **`flatten` unifies two front-ends.** Native `tool_use` puts params in `Input` (a decoded map); s02's XML parser puts them in `Params` (a string map). `flatten` merges both into one `map[string]string`, so a handler never has to care which front-end parsed the call.
3. **Validation returns *which* param is missing, not a boolean.** The error names the specific param (`missing required parameter "content"`) so the model can correct precisely instead of guessing.
4. **`CallID` threads through every exit.** Both success and failure `ToolResult`s carry `block.CallID`, so the `tool_result` block pairs with the original `tool_use` — the Anthropic protocol requires this so the model knows which call a result answers.

## What Changed (vs. s02)

```diff
- // s01/s02: a parsed tool_use was either run by one inline tool, or just
- // surfaced as a block. There was no name-based routing.
- block := parser.Blocks()[i]          // s02 gives us parsed blocks ...
- // ... and then? s02 stops at parsing.

+ // s03: parsed blocks are now DISPATCHED through a registry.
+ reg := NewRegistry()
+ reg.Register(&ReadFileTool{Base: base})
+ reg.Register(&WriteToFileTool{Base: base})
+ reg.Register(&ListFilesTool{Base: base})
+ reg.Register(&ExecuteCommandTool{Base: base})
+ exec := NewToolExecutor(reg)
+
+ result := exec.Execute(ctx, block)   // name → handler → tool_result
```

The semantic difference: s02 stopped at "a `tool_use` block was parsed" — it produced blocks but didn't know what to do with one. s03 connects the next stage: those blocks are now **dispatched by name** to handlers. Tool execution moves from "one inline tool" to "many coexisting in a registry", and registering a new tool no longer touches dispatch logic — just call `Register`. That is precisely how cline holds ~27 tools plus runtime MCP tools: they are all just entries in a map.

## Try It

```bash
cd agents/s03-tool-registry-execution

# The scripted dispatch sequence: write, read back, list dir, run a command,
# then two failure cases.
go run .

# Also print each registered tool's schema to stderr.
go run . -v

# Tests (fully offline, t.TempDir sandbox).
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape (s03 is byte-stable — the temp sandbox path never appears):

```
[t1] write_to_file    -> ok    wrote 11 bytes to notes/hello.txt
[t2] read_file        -> ok    hi from s03
[t3] list_files       -> ok    hello.txt
[t4] execute_command  -> ok    registry-and-dispatch
[t5] fly_to_moon      -> ERROR Error: unknown tool "fly_to_moon". ...
[t6] read_file        -> ERROR Error: missing required parameter "path" ...
```

How to tell you ran it right: t1..t4 are all `ok` (the four real tools dispatched and ran); t2 reads back what t1 wrote (a write→read round-trip through the registry); t5 is an `unknown tool` error; t6 is a `missing required parameter` error. Both errors return an `IsError=true` `tool_result`, the loop keeps going, and the model can see them — nothing panics.

## Upstream Source Reading

The equivalent mechanism upstream lives in two files: the registry itself in `ToolExecutorCoordinator.ts` (a `Map<name, handler>` with register/has/getHandler/execute), and the dispatch entry the Task loop actually calls in `ToolExecutor.ts`'s `execute` (L312) — which does a `has()` check first, then a try/catch that turns any failure into a `tool_result`. Here is the registry core:

```upstream:apps/vscode/src/core/task/tools/ToolExecutorCoordinator.ts#L111-L168
// THE registry. ~27 handlers live here, keyed by tool name.
export class ToolExecutorCoordinator {
	private handlers = new Map<string, IToolHandler>()

	// register: store a handler under its own .name. registerToolHandlers()
	// (ToolExecutor.ts L201) calls this in a loop over every known tool name.
	register(handler: IToolHandler): void {
		this.handlers.set(handler.name, handler)
	}

	// has: is this tool name registered? The orchestrator calls this FIRST so an
	// unknown tool is handled gracefully instead of dispatching into nothing.
	has(toolName: string): boolean {
		return this.getHandler(toolName) !== undefined
	}

	getHandler(toolName: string): IToolHandler | undefined {
		// (real code also remaps MCP tool names + builds dynamic subagent
		//  handlers here — elided; s03 has neither.)
		return this.handlers.get(toolName)
	}

	// execute: resolve + run. THROWS on unknown tool — the caller
	// (ToolExecutor.execute) turns that into a tool_result via handleError.
	async execute(config: TaskConfig, block: ToolUse): Promise<ToolResponse> {
		const handler = this.getHandler(block.name)
		if (!handler) {
			throw new Error(`No handler registered for tool: ${block.name}`)
		}
		return handler.execute(config, block)
	}
}
```

**Reading notes**:

- **How registration happens**: upstream uses `registerByName(toolName, validator)` driven by a `toolHandlersMap` factory table to `new` each handler; we just `reg.Register(&ReadFileTool{...})`. The only difference is whether there's a factory layer in between — the map itself is identical.
- **Throw vs. return**: upstream's `coordinator.execute` *throws* on an unknown tool, and the outer `ToolExecutor.execute` try/catch + `handleError` converts it to a `tool_result`. We merge those two steps and return the error result directly — Go has no exceptions, so explicit returns read more naturally.
- **Where param validation sits**: upstream each handler calls `ToolValidator.assertRequiredParams` at the top of its own `execute`; we lift validation into the dispatcher (using each handler's declared `RequiredParams()`). Same semantics: reject a missing-param call before executing.
- **Fields we drop**: upstream's `getHandler` also normalizes MCP tool names to a single handler and lazily builds dynamic subagent handlers; s03 has neither MCP (s09) nor subagents, so it's elided.
- **A deliberately "correct but imperfect" choice we keep**: `execute_command` runs via `/bin/sh -c` with no approval. That's right for s03 — this chapter is only about the registry and dispatch; the human-in-the-loop gate for dangerous commands is s04's subject.

**Read further**: start at `ToolExecutor.ts` `registerToolHandlers` (L201) to see the registry get filled, then `executeTool` (L212) → `execute` (L312) for per-call dispatch; follow `coordinator.execute` (L162) into `handlers/ReadFileToolHandler.ts`, finally `ToolValidator.ts`'s `assertRequiredParams` (L17). The approval layer wrapping `handler.execute` (`askApproval` → `autoApprove.ts shouldAutoApproveTool`) is s04. That trace is the real-source map for s03 → s04.

---

**Next**: s04 inserts an **approval gate** between dispatch and `Execute`. The same tool calls now each pass a policy + human checkpoint first — read-style tools auto-approve, while writes and shell commands ask.
