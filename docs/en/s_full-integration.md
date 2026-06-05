---
title: "Full Integration · Wiring Ten Chapters Into One cline Agent"
chapter: full
slug: s_full-integration
est_read_min: 16
---

# Full Integration · Wiring Ten Chapters Into One cline Agent

> This chapter introduces no new mechanism. It takes the parts each of the first ten chapters (s01–s10) polished in isolation and strings them, in the real order cline's `Task` uses, into one pipeline — answering a single question: **how do these separate little modules, put together, actually become an agent that edits code on its own?** By the end you should be able to draw the "provider stream → loop → parse → tools → approval" spine from memory and name the exact Go function each step maps to.

---

## Architecture Overview

cline's core boils down to one sentence: **an agent is not the LLM call, it is the loop around it.** A model that says "I want to read this file" just stops there — unless something actually reads it, feeds the result back, and asks again. That "ask again with the result" *is* the loop (s01), and the other nine chapters are capabilities hung off it.

Here is the whole mini stack. Solid lines are the spine every turn travels; dashed lines are side capabilities triggered on demand.

```
            ┌──────────────────────────────────────────────────────────┐
            │                     Task loop  (s01)                       │
            │             loop.go: Task.Run — the per-turn engine        │
            └───┬───────────────┬───────────────┬──────────────────┬────┘
                │ 1.request      │ 3.parse        │ 5.dispatch       │ 8.next turn
                ▼               ▼               ▼                  │
      ┌───────────────┐ ┌──────────────┐ ┌──────────────┐         │
      │ Provider (s05)│ │ Parser (s02) │ │ Registry(s03)│         │
      │ Anthropic SSE │ │  incremental │ │  by-name     │         │
      └───────┬───────┘ └──────────────┘ └──────┬───────┘         │
              │ 2.chunk stream                   │ 4.approval gate  │
              │                                  ▼                  │
        ┌─────┴──────┐                   ┌──────────────┐          │
        │ Prompt (s06)│··· tools into ··▶│ Approval(s04)│          │
        │  modular    │     the prompt    │ auto / human │          │
        └────────────┘                   └──────┬───────┘          │
                                          6.allow │ run            │
                ┌─────────────────────────┬──────┴──────┐          │
                ▼                         ▼             ▼          │
        ┌──────────────┐         ┌──────────────┐ ┌──────────┐    │
        │ File edit(s07)│         │ MCP tool(s09)│ │ other tool│    │
        │ SEARCH/REPLACE│         │ external proc │ │read/exec │    │
        └──────────────┘         └──────────────┘ └──────────┘    │
                │ 7.commit each turn                                │
                ▼                                                  │
        ┌──────────────┐         ┌──────────────┐                 │
        │ Checkpt (s10) │         │ Context (s08)│◀────────────────┘
        │  shadow git   │         │ trim if over │  9.prune before next turn
        └──────────────┘         └──────────────┘
```

One-line pitch: **s01 is the skeleton; s05/s02 turn the model's words into structured blocks; s03/s04 decide which tool may run and whether a human must nod; s06 tells the model what tools exist; s07/s09 are the hands that do real work; s08/s10 quietly keep "history from overflowing" and "any step reversible."** Wire them as above and what you get is not a pile of parts — it is the cline agent itself.

---

## Execution Trace

Here is one real task walked through the whole loop: the user says **"add a function to `greet.go` and verify it compiles."** Each step cites the Go file and function it maps to in the repo (all verified to exist). This trace is a mini-mapping of the main loop in cline's `apps/vscode/src/core/task/index.ts` (`recursivelyMakeClineRequests`), and corresponds to the A3 16-step end-to-end trace in the research notes.

1. **Build the prompt.** First pick a variant by model family (Claude-4 → next-gen native tools; everything else → generic XML), then render the s03 registry's tool specs into the `TOOL USE` section. → `agents/s06-system-prompt/variants.go` (`SelectVariant`) + `prompt.go` (`PromptBuilder.Build`).

2. **Open the loop.** `Task.Run` seeds the user's sentence as the first `user` message and enters the `for turn < MaxTurns` engine; `MaxTurns` is the hard stop that keeps a confused model from spinning forever. → `agents/s01-minimum-agent-loop/loop.go` (`Task.Run`).

3. **Send the request, stream it back.** The loop hands `system prompt + history + tool specs` to the provider, `POST /v1/messages` with `stream:true`, and starts reading Server-Sent Events. → `agents/s05-provider-streaming/provider.go` (`AnthropicProvider.CreateMessageStream`).

4. **Decode SSE, normalize chunks.** `decodeAnthropicSSE` translates vendor events (`content_block_start / content_block_delta / message_stop`) into one uniform `StreamChunk` type (text / tool_use_start / tool_use_delta / usage / done) — the loop only ever sees this one chunk shape, never a vendor format. → `agents/s05-provider-streaming/provider.go` (`decodeAnthropicSSE`).

5. **Parse the assistant message incrementally.** Text deltas feed the streaming parser; it re-derives `text` and `tool_use` blocks from the accumulated buffer, flagging the trailing block `partial` when the buffer cuts mid-tag. This turn, the model decides to call `replace_in_file`. → `agents/s02-streaming-message-parser/parser.go` (`StreamingParser.feed` / `parse`).

6. **Dispatch to the registry.** The parsed `tool_use` block goes to the executor, which looks the handler up by name in the registry; a miss does not panic — it returns an `IsError` `tool_result` so the model can self-correct. → `agents/s03-tool-registry-execution/executor.go` (`ToolExecutor.Execute`) + `tools.go` (`Registry.Get`).

7. **Through the approval gate (auto-approve fast path).** `replace_in_file` is a write, so policy is asked first: a path inside the workspace plus the tool being on the allowlist auto-clears it; otherwise it escalates to a human. Read-only tools like `read_file` get auto-approved here without ever prompting. → `agents/s04-approval-gating/approval.go` (`AutoApprovePolicy.shouldAutoApprove`).

8. **Through the approval gate (human escalation).** When policy abstains it escalates to the human; this two-stage decision (policy first, then human) is the heart of gating, and a rejection does not abort the task — it returns a feedback `tool_result`. → `agents/s04-approval-gating/gate.go` (`ApprovalGate.decide` / `ApprovalGate.Execute`).

9. **Apply the file edit.** Once approved, `replace_in_file` locates the `------- SEARCH / ======= / +++++++ REPLACE` block against the original, splices in the replacement, and writes it back to disk (inside the sandbox). → `agents/s07-file-edit-diff/diff.go` (`constructNewFileContent`) + `edit.go` (`applyDiffToFile`).

10. **Produce the tool_result.** The gate wraps the tool's output (or error text) into a `tool_result` block carrying `CallID` — that id lets the model match the result back to the `tool_use` it just emitted. → `agents/s04-approval-gating/gate.go` (`ApprovalGate.Execute`).

11. **Checkpoint this turn.** After files change, the shadow git does a `git add -A` + `git commit --allow-empty`; the returned hash is recorded as this turn's `Checkpoint`, and the whole timeline stays invisible to the user's real `.git`. → `agents/s10-checkpoints-shadow-git/checkpoint.go` (`ShadowGit.Commit`).

12. **Append to history, loop.** The loop appends this turn's `assistant` message and the `tool_result` (as the next `user` message) to history, then goes back to step 3 for the next request. → `agents/s01-minimum-agent-loop/loop.go` (`Task.Run`'s `runTools`).

13. **Prune context before the next turn.** Before sending the next request, estimate tokens; if over budget, compute a range of middle messages to drop (always keep the first user/assistant pair and the most recent turns) and strip any orphaned `tool_result` off the new first message. → `agents/s08-context-window-management/context.go` (`estimateTokens` / `ContextManager.overBudget` / `truncate`).

14. **Turn two: the model wants to verify.** Repeat steps 3–6; this time the model calls `execute_command` to run `go build ./...`. That is a separate tool, pulled from the registry by name just the same. → `agents/s03-tool-registry-execution/tools.go` (`Registry.Get`, `ExecuteCommandTool`).

15. **Completion detection.** The model sees the build pass, emits no `tool_use` this turn, and `stop_reason` is `end_turn`; the loop reads that as task complete and returns the final text. → `agents/s01-minimum-agent-loop/loop.go` (`Task.Run`'s `end_turn` branch).

16. (optional) **Undo.** If the user later wants to revert, `git reset --hard` to some `Checkpoint`'s hash snaps the workspace files back to that turn's state, and the timeline can continue forward from there. → `agents/s10-checkpoints-shadow-git/checkpoint.go` (`ShadowGit.Restore`).

> The MCP tool (s09) in step 9 above was not triggered in this trace, but it is a side branch off the same dispatch point: an external MCP server's tool, once adapted into an ordinary `Tool` via `DiscoverTools`, is registered into the same table, after which step 6 treats it exactly like `read_file`. → `agents/s09-mcp-integration/mcp.go` (`McpClient.ListTools` / `DiscoverTools` / `CallTool`).

---

## Cross-chapter Interaction

The sequence diagram below traces message flow between objects in the real order of one turn. The point is to separate **stable contracts** (data shapes unchanged across chapters, safe to swap implementations behind) from **dynamic dispatch** (branches decided only at runtime).

```
User          Task loop     Provider      Parser        Executor      Approval     Tool/Edit    Shadow git
(s01)         (s05)        (s02)         (s03)         (s04)        (s07/s09)    (s10)
 │             │            │             │             │             │            │
 │ task text   │            │             │             │             │            │
 ├────────────▶│            │             │             │             │            │
 │             │ CreateMessageStream(req) │             │             │            │
 │             ├───────────▶│             │             │             │            │
 │             │  «StreamChunk» stream ◀─ │             │             │            │   ← stable contract: StreamChunk
 │             │◀───────────┤             │             │             │            │     (text/tool_use/usage/done)
 │             │ feed(text delta)         │             │             │            │
 │             ├─────────────────────────▶│             │             │            │
 │             │  «AssistantBlock» ◀───── │             │             │            │   ← stable contract: AssistantBlock
 │             │◀─────────────────────────┤             │             │            │     (type/name/params/partial)
 │             │ Execute(toolUse)         │             │             │            │
 │             ├──────────────────────────────────────▶│             │            │
 │             │              registry.Get(name) ★ dynamic dispatch: handler by name             │
 │             │                                        │ decide(name,params)      │
 │             │                                        ├────────────▶│            │
 │             │                       ★ dynamic dispatch: policy→human → allow/reject            │
 │             │                                        │◀────────────┤            │
 │             │                                        │ tool.Execute(input)      │
 │             │                                        ├──────────────────────────▶│  (s07 edits file / s09 calls MCP)
 │             │                                        │◀──────────────────────────┤
 │             │   «ToolResult» (CallID, output) ◀──── │             │            │   ← stable contract: ToolResult
 │             │◀──────────────────────────────────────┤             │             │     (call_id matches tool_use)
 │             │ Commit("turn N")                       │             │             │
 │             ├───────────────────────────────────────────────────────────────────▶│
 │             │   hash ◀────────────────────────────────────────────────────────── │
 │             │ overBudget? → truncate(history)  (s08, before next turn)             │
 │             │ append assistant + tool_result to history, loop ↺                    │
```

Three things to take from the diagram:

- **Three stable contracts** run end to end and are why the chapters decouple cleanly. The `Provider` interface (`CreateMessageStream` returns a channel of `StreamChunk`) keeps the loop indifferent to which vendor answered; `AssistantBlock` is the parser's sole output (`type/name/params/partial`), so s02 and s03 talk through this one shape only; `ToolResult` carries `CallID`, matching a tool's result firmly back to the `tool_use` the model emitted. Swap any chapter's internals and, as long as these three shapes hold, the loop changes not one line.
- **Two dynamic-dispatch points** are branches decided only at runtime — exactly where "the agent varies." First, `registry.Get(name)`: one `Execute` entry, pulling a different handler depending on which name the model reported this turn (`read_file` vs `replace_in_file` vs some MCP tool). Second, the approval decision: `decide` asks policy then human, and the result (auto-approve / human-approved / rejected) can differ every time, with a rejection folded back into the model as feedback rather than aborting.
- **Side branches share the spine's exit**: s07's file edit and s09's MCP tool do not each open their own path in the loop — they all leave through the single `tool.Execute` door. That is precisely the value of the s03 registry: a new capability is just one more `Tool` in the table, and the spine stays untouched.

---

## Deliberate Omissions

This integration keeps only the spine needed to run one real turn end to end. cline's real product carries many features just as important, but they would drown the teaching code, so this curriculum implements none of them. The table lists them and why they are left out, so you know the lay of the land when you later read the upstream source.

| Omitted cline feature | Upstream location (reference) | Why not here |
|---|---|---|
| Plan / Act dual-mode UI | `PlanModeRespondHandler.ts` / `ActModeRespondHandler.ts` | This is the product layering of "when approval matters" (explore first, then execute) — an interaction policy. The core loop only needs "every side-effecting tool passes the gate"; mode switching does not change the loop skeleton. See Appendix A. |
| Webview / gRPC front-end | `apps/vscode/src/core/controller/` + `webview-ui/` | The front-end is just one client of the loop, replaceable by a CLI or another UI; the teaching version uses stdin/stdout and injected decisions instead, avoiding a whole IPC stack. |
| Browser tool | `services/browser/BrowserSession.ts` | Driving a headless browser is heavy and orthogonal to the "agent loop" theme; it is merely one more tool in the registry, a pattern already covered by s03/s09. |
| Terminal integration (long-running procs / output streaming) | `integrations/terminal/CommandExecutor.ts` | s03's `execute_command` runs one-shot commands; real PTY management, streaming stdout, and interrupt signals are a whole terminal-engineering effort beyond what the spine needs. |
| Focus chain / task to-dos | `apps/vscode/src/core/task/` state machine | This is the product ability to split a long task into trackable subgoals — an orchestration layer above the loop; it does not affect the "request → tool → result" spine. |
| Telemetry / observability | `posthog-node`, `@opentelemetry/*` | Instrumentation and tracing are operational concerns that add nothing to understanding the mechanism and only sprinkle each function with irrelevant branches. |
| The full 40+ provider list | `apps/vscode/src/core/api/providers/` (40+ files) | s05 does only Anthropic SSE plus one OpenAI-compatible path, enough to demonstrate "normalize to one chunk type + factory selection"; the rest are repetitions of the same pattern. |
| `.clinerules` project rules | workspace `.clinerules` / `.clinerules/` | This is the ability to feed team conventions declaratively into the system prompt; s06 already shows how the prompt is assembled modularly, and a rules file is just one more source to inject. |
| Context condensing (summarization) | the summarize path in `ContextManager.ts` | s08 implements only "truncation" (drop old messages); "call the model once to summarize old history into a paragraph" is a costlier, more complex lossy compression left as an extension. |
| MCP SSE/HTTP transport & OAuth | `services/mcp/McpHub.ts` (~1500 LOC) | s09 does only stdio JSON-RPC, enough to show "how an external process's tool becomes a local `Tool`"; auth and multi-transport are production details, not the mechanism itself. |

These are omitted not because they are unimportant, but because **the skeleton of the mechanism is clearest without them.** Treat the table as a "what to read next" map: each row is a direction you can now dive into independently, having understood the spine.

---

## Read Further

- **Appendix A: approval safety model** — puts s04's gate back in the Plan/Act context, explaining why every destructive tool defaults to requiring a human nod, and how a rejection round-trips as feedback rather than aborting the task.
- **Appendix B: upstream map** — a lookup table mapping each chapter back to cline's real source file and symbol; after building the toy, read the original in `apps/vscode/src/core/` with it in hand.
- **The upstream main loop** — `initiateTaskLoop` (L1453) and `recursivelyMakeClineRequests` (L2354) in `apps/vscode/src/core/task/index.ts`: the full, production-grade prototype of this chapter's 16-step trace (with cancellation, locks, presentation scheduling, and the rest this course omits).
- **Research notes A3** — the 16-step end-to-end trace in section 8 of `.learn/research-notes.md`, running from CLI args all the way to session persistence — the real-CLI counterpart to this chapter's Execution Trace.
- **Per-chapter revisit** — if any step is unclear, return to its chapter doc (`docs/en/s01`…`s10`): each explains its mechanism in full via the same problem → solution → how-it-works → upstream-mapping format.
