---
title: "Appendix B · Upstream Map"
appendix: B
slug: appendix-b-upstream-map
est_read_min: 10
---

# Appendix B · Upstream Map

> A reference for reading the *real* cline source after you build the toy. Every path and line number below is pinned to [cline](https://github.com/cline/cline) @ `a209825116dca469c80af4be53989638dd329f38`. Teaching target throughout: `apps/vscode/src/core/` (the original VS Code-extension agent), not the newer `sdk/packages/` headless SDK.

## Reading order

cline's `task/index.ts` is ~3,800 lines; do not start there. Read the pieces in the order you built them — each chapter's mini gives you the vocabulary for one upstream file, so by the end the big loop reads as "the parts I already know, wired together."

1. [`task/index.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts) — `initiateTaskLoop` (L1453) → `recursivelyMakeClineRequests` (L2354). The loop shape. **(s01)** Skim only.
2. [`assistant-message/parse-assistant-message.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/parse-assistant-message.ts) — `parseAssistantMessageV2` (L28). How a growing string becomes tool calls. **(s02)**
3. [`task/ToolExecutor.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts) — `registerToolHandlers` (L201), `executeTool` (L212), coordinator dispatch (L575). The registry. **(s03)**
4. [`task/tools/autoApprove.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts) — `shouldAutoApproveTool` (L42). The safety gate. **(s04, Appendix A)**
5. [`api/providers/anthropic.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/api/providers/anthropic.ts) — `createMessage` (L64) + [`api/index.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/api/index.ts) `buildApiHandler` (L478). Real streaming behind a factory. **(s05)**
6. [`prompts/system-prompt/index.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/prompts/system-prompt/index.ts) — `getSystemPrompt` (L16). Modular, variant-driven prompt. **(s06)**
7. [`assistant-message/diff.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/diff.ts) — `constructNewFileContent` (L245), `constructNewFileContentV2` (L823). SEARCH/REPLACE application. **(s07)**
8. [`context/context-management/ContextManager.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/context/context-management/ContextManager.ts) — `getNextTruncationRange` (L299). Budgeted truncation. **(s08)**
9. [`services/mcp/McpHub.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/services/mcp/McpHub.ts) — `connectToServer` (L286), `callTool` (L1233). Dynamic external tools. **(s09)**
10. [`integrations/checkpoints/CheckpointTracker.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/integrations/checkpoints/CheckpointTracker.ts) — `create` (L127), `commit` (L212), `resetHead` (L336). Shadow-git undo. **(s10)**

## File-to-session map

Each row is verified to exist at the pinned sha. "Lines" point to the symbol that anchors the chapter.

| Upstream file | Lines | What it does | Our session |
|---------------|-------|--------------|-------------|
| `apps/vscode/src/core/task/index.ts` | L1453-1466 | `initiateTaskLoop` — seeds the recursive request loop | s01 |
| `apps/vscode/src/core/task/index.ts` | L2354 | `recursivelyMakeClineRequests` — one turn: request → stream → tools → recurse | s01 |
| `apps/vscode/src/core/task/index.ts` | L661 | `Task.ask` — pause and request human input (the approval transport) | s04, App. A |
| `apps/vscode/src/core/task/index.ts` | L826 | `Task.say` — stream status/output to the UI | s04, App. A |
| `apps/vscode/src/core/assistant-message/parse-assistant-message.ts` | L28-240 | `parseAssistantMessageV2` — incremental XML tool-call parser | s02 |
| `apps/vscode/src/core/assistant-message/index.ts` | L3-81 | `AssistantMessageContent` / `ToolUse` union + `toolParamNames` | s02 |
| `apps/vscode/src/core/task/ToolExecutor.ts` | L201-205 | `registerToolHandlers` — register every handler with the coordinator | s03 |
| `apps/vscode/src/core/task/ToolExecutor.ts` | L212 | `executeTool` — public entry the Task loop calls per tool block | s03 |
| `apps/vscode/src/core/task/ToolExecutor.ts` | L575 | `coordinator.execute` — name-dispatch to the resolved handler | s03 |
| `apps/vscode/src/core/task/tools/autoApprove.ts` | L42-117 | `shouldAutoApproveTool` — per-tool auto-approve whitelist | s04, App. A |
| `apps/vscode/src/core/task/tools/autoApprove.ts` | L122-167 | `shouldAutoApproveToolWithPath` — whitelist + workspace-path check | s04, App. A |
| `apps/vscode/src/core/task/tools/types/UIHelpers.ts` | L56-59 | `askApproval` — reduces webview answer to a yes/no | s04, App. A |
| `apps/vscode/src/core/task/tools/handlers/PlanModeRespondHandler.ts` | L15-58 | Plan-mode reply tool (`PLAN_MODE`) | App. A |
| `apps/vscode/src/core/task/tools/handlers/ActModeRespondHandler.ts` | L10-30 | Act-mode reply tool (`ACT_MODE`) | App. A |
| `apps/vscode/src/core/api/providers/anthropic.ts` | L64-300 | `createMessage` — Anthropic SSE stream → `ApiStream` chunks | s05 |
| `apps/vscode/src/core/api/index.ts` | L76 | `createHandlerForProvider` — provider switch | s05 |
| `apps/vscode/src/core/api/index.ts` | L478 | `buildApiHandler` — the provider factory | s05 |
| `apps/vscode/src/core/prompts/system-prompt/index.ts` | L16-21 | `getSystemPrompt` — entry to the modular prompt builder | s06 |
| `apps/vscode/src/core/assistant-message/diff.ts` | L1-3, L245, L823 | SEARCH/REPLACE markers + `constructNewFileContent` / `...V2` | s07 |
| `apps/vscode/src/core/context/context-management/ContextManager.ts` | L227 | `getNewContextMessagesAndMetadata` — assemble + maybe truncate | s08 |
| `apps/vscode/src/core/context/context-management/ContextManager.ts` | L299-348 | `getNextTruncationRange` / `getAndAlterTruncatedMessages` | s08 |
| `apps/vscode/src/services/mcp/McpHub.ts` | L286, L677 | `connectToServer` / `fetchToolsList` | s09 |
| `apps/vscode/src/services/mcp/McpHub.ts` | L1233 | `callTool` — invoke a remote MCP tool | s09 |
| `apps/vscode/src/integrations/checkpoints/CheckpointTracker.ts` | L127, L212, L336 | `create` / `commit` / `resetHead` | s10 |
| `apps/vscode/src/integrations/checkpoints/CheckpointGitOperations.ts` | L59 | `initShadowGit` — separate git dir over the workspace tree | s10 |

## Symbol cross-reference

Our Go names against their upstream TypeScript equivalents. (cline's loop is one big `Task` class; our vocabulary splits it into small types — so several rows map a Go type to a *method or field* of `Task`/`ToolExecutor`.)

| Our type/func | Upstream equivalent | File:line |
|---------------|---------------------|-----------|
| `Task.Run` | `Task.recursivelyMakeClineRequests` / `initiateTaskLoop` | `task/index.ts:2354,1453` |
| `Message` / `ContentBlock` | Anthropic Messages wire shape (`ClineStorageMessage`) | `api/providers/anthropic.ts:64` |
| `AssistantMessageParser` | `parseAssistantMessageV2` | `assistant-message/parse-assistant-message.ts:28` |
| `AssistantMessageBlock` | `AssistantMessageContent` / `ToolUse` | `assistant-message/index.ts:3,63` |
| `ToolExecutor` | `ToolExecutor` (class) + `registerToolHandlers` | `task/ToolExecutor.ts:43,201` |
| `ToolExecutor.Execute` | `ToolExecutor.executeTool` → `coordinator.execute` | `task/ToolExecutor.ts:212,575` |
| `Tool` (interface) | `IToolHandler` (handlers/*) | `task/tools/handlers/ReadFileToolHandler.ts` |
| `Approver` | `askApproval` → `Task.ask` | `task/tools/types/UIHelpers.ts:56` |
| `AutoApprovePolicy` | `AutoApprove` (class) | `task/tools/autoApprove.ts:8` |
| `AutoApprovePolicy.shouldAutoApprove` | `shouldAutoApproveTool` | `task/tools/autoApprove.ts:42` |
| `ToolPolicy{AutoApprove, AutoApproveExternal}` | `[local, external]` tuple | `task/tools/autoApprove.ts:96,163` |
| `Decision` (enum) | `didRejectTool` flag + `yesButtonClicked` | `task/ToolExecutor.ts`, `UIHelpers.ts:58` |
| `Provider.Stream` / `StreamEvent` | `ApiHandler.createMessage` / `ApiStreamChunk` | `api/providers/anthropic.ts:64` |
| `buildProvider` (factory) | `buildApiHandler` / `createHandlerForProvider` | `api/index.ts:478,76` |
| `PromptBuilder` / `getSystemPrompt` | `PromptBuilder` / `getSystemPrompt` | `prompts/system-prompt/index.ts:16` |
| `constructNewFileContent` | `constructNewFileContent` / `...V2` | `assistant-message/diff.ts:245,823` |
| `ContextManager.getNextTruncationRange` | `ContextManager.getNextTruncationRange` | `context/context-management/ContextManager.ts:299` |
| `McpHub` (connect/listTools/callTool) | `McpHub.connectToServer` / `fetchToolsList` / `callTool` | `services/mcp/McpHub.ts:286,677,1233` |
| `Checkpoint` + shadow git | `CheckpointTracker.create` / `commit` / `resetHead` | `integrations/checkpoints/CheckpointTracker.ts:127,212,336` |
| (shadow-git init) | `GitOperations.initShadowGit` | `integrations/checkpoints/CheckpointGitOperations.ts:59` |

## Suggested exercises

1. **Find a tool we didn't port.** Open [`task/tools/handlers/`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers) and pick one (e.g. `SearchFilesToolHandler` or `BrowserToolHandler`). Trace where its name is registered (`registerToolHandlers`, ToolExecutor.ts L201) and where its auto-approve policy lives (autoApprove.ts L42). Add the equivalent tool to your s03/s04 registry.
2. **Follow a rejection end-to-end.** Start at `WriteToFileToolHandler` (the `ask` at L79), into `Task.ask` (task/index.ts L661), and find where `didRejectTool` (ToolExecutor.ts L325) turns the denial into `formatResponse`-shaped feedback rather than aborting. Compare with `gate.Execute` in your s04.
3. **Diff v1 vs v2.** Read both `constructNewFileContent` (diff.ts L245) and `constructNewFileContentV2` (L823) and list what v2 added (hint: streaming `isFinal`, fuzzy matching). Which behaviors did s07 port, which did it summarize?
4. **Trace the native vs XML tool-call split.** s02 parses XML; native tool calls take a different path. Find where `createMessage` (anthropic.ts L64) emits tool-call deltas and contrast with `parseAssistantMessageV2`. Where would a Claude-4 model diverge from a generic one?
5. **Map the truncation math.** Read `getNextTruncationRange` (ContextManager.ts L299) and confirm the "keep the first user/assistant pair, remove an even count, escalate to 3/4" rule your s08 tests assert.

## Caveats

- **Line numbers are pinned to `a209825116dca469c80af4be53989638dd329f38`.** cline moves fast (the research dossier notes multiple releases within 30 days). If you `git pull` upstream, the symbol *names* will still be findable but the *line numbers* in every table above will drift — search by symbol, not by line.
- **Two agents in one monorepo.** Everything here targets `apps/vscode/src/core/` (the canonical VS Code agent). The `sdk/packages/` tree is a separate, newer headless SDK with its own `agent-runtime.ts`; its mechanism *names* overlap but its line numbers do not. Don't cross the streams.
- **`task/index.ts` is ~3,800 lines.** The s01 anchors (L1453, L2354) are the *loop shape* only; the surrounding cancellation, focus-chain, locking, and presentation-scheduling code is deliberately out of scope for the curriculum.
- **`diff.ts` ships v1 and v2.** s07 teaches v2 (L823) semantics; v1 (L245) remains as the dispatcher's fallback. Citing one without the other will confuse a reader diffing the file.
- **MCP and checkpoints are large.** `McpHub.ts` (~1,500 LOC) and the checkpoints integration include OAuth, transports, and multi-root handling the toys skip. The cited symbols are the load-bearing core, not the whole file.
