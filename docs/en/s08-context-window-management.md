---
title: "s08 · Context Window Management"
chapter: 8
slug: s08-context-window-management
est_read_min: 12
---

# s08 · Context Window Management

> What this teaches: **context-window management** — how cline keeps a growing conversation under the model's token budget by truncating OLD middle messages while always keeping the first task message and the most recent turns, without breaking the `tool_use`/`tool_result` pairing the API requires.

---

## Problem

Every chapter so far has let the conversation grow without limit. The agent loop (s01) appends a user turn, an assistant turn, then a user turn carrying the `tool_result` — turn after turn. The tool registry (s03) and file-edit diff (s07) make each turn *fatter*: a single `read_file` or a `final_file_content` block can be thousands of characters. After a dozen rounds of reading and editing files, that history is enormous.

But every model has a fixed **context window** (~200K tokens for Claude 4, far less for small or local models), and the API rejects a request that exceeds it. So the agent that was happily editing files suddenly fails — not because the task is hard, but because it *remembers too much*. The naive fixes are both wrong: dropping the **oldest** messages deletes the task definition the model needs to stay on-target; dropping the **newest** deletes the work it's in the middle of. And carelessly slicing the list can orphan a `tool_result` whose matching `tool_use` was removed, which the API also rejects. This chapter solves all three.

## Solution

Treat the history as a budgeted list and trim from the **middle**. The mental model is a sandwich: keep the first user/assistant pair (the task, the model's north star), keep the most recent turns (where it's actively working), and drop the stale middle that has already served its purpose.

Three design decisions carry the chapter:

1. **Estimate, then trigger.** A char/4 heuristic (`estimateTokens`) approximates the request's cost by serializing system + messages + tools. When that estimate exceeds the `Budget`, truncation runs. cline triggers on the *previous* request's real usage; we estimate the request we're about to send. Either way it's a single comparison.
2. **Keep the first pair, drop an even middle range, end on an assistant.** `nextTruncationRange` always starts the deletion at index 2 (indices 0–1 are the task and are never touched), removes a count computed as even (whole pairs), and backs off by one if the last removed message isn't an assistant — so the kept tail resumes on a *user* message, preserving the strict user-assistant-user-assistant order.
3. **Strip orphaned tool_results at the seam.** After splicing `[first pair] + [tail]`, the new first tail message may be a user turn whose `tool_result` references a now-deleted `tool_use`. `applyTruncation` filters those blocks out so the request stays valid.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  estimateTokens(System + Messages + Tools)  >  Budget ?         │
│        │ no                              │ yes                   │
│        ▼                                 ▼                       │
│  return unchanged          nextTruncationRange(messages)         │
│                              start=2 (keep first pair)           │
│                              remove even middle, end on assistant│
│                                          │                       │
│                                          ▼                       │
│  applyTruncation:  [0,1]  +  messages[end+1:]                    │
│                              └─ strip orphaned tool_results ─┐   │
│                                                             ▼   │
│   [ task pair ] ............(dropped).......... [ recent tail ] │
└────────────────────────────────────────────────────────────────┘
```

The core ~30-line excerpt (from [`agents/s08-context-window-management/context.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s08-context-window-management/context.go)) — the range math that decides what to drop:

```go
func (cm *ContextManager) nextTruncationRange(messages []Message) (start, end int, ok bool) {
	const rangeStart = 2 // index 0 and 1 are the first user/assistant pair — kept

	n := len(messages)
	if n <= rangeStart+cm.KeepRecent {
		return 0, 0, false // too few messages to drop anything safely
	}

	startOfRest := rangeStart
	lastKeepable := n - cm.KeepRecent // first index of the recent tail to PRESERVE
	droppable := lastKeepable - startOfRest
	if droppable <= 0 {
		return 0, 0, false
	}

	messagesToRemove := droppable
	if messagesToRemove%2 != 0 {
		messagesToRemove-- // aim for whole pairs (upstream's even count)
	}
	if messagesToRemove <= 0 {
		return 0, 0, false
	}

	end = startOfRest + messagesToRemove - 1 // inclusive end

	// Ensure the LAST removed message is an assistant message, so the kept tail
	// resumes on a user message (upstream's rangeEndIndex -= 1 guard).
	if end >= 0 && end < n && messages[end].Role != "assistant" {
		end--
	}
	if end < startOfRest {
		return 0, 0, false
	}
	return startOfRest, end, true
}
```

**Four non-obvious points**:

1. **The first pair is sacred.** `rangeStart` is hard-coded to 2. The task definition (and the assistant's first acknowledgement) anchor the whole session; deleting them makes the model wander even though the *recent* context looks fine.
2. **Even count, but the guard can break parity — on purpose.** The count is computed even (whole pairs), yet the assistant-end guard may decrement it to odd. That's intentional: the load-bearing invariant is the *boundary role* (kept tail starts on a user message), not how many were removed.
3. **Orphaned tool_results would crash the request.** A `tool_result` block must follow its `tool_use`. If truncation removes the assistant turn that issued the call but keeps the user turn with the result, the API errors. `applyTruncation` strips those leftovers from the seam message.
4. **One pass may not be enough.** Truncation removes a bounded middle once per request. If the result is still over budget (lots of fat recent turns), cline simply truncates again on the *next* request — it doesn't loop. The `-budget 1500` demo shows exactly this.

## What Changed (vs. s07)

Through s07, history was append-only: every tool result — including s07's full `final_file_content` blocks — was added and never removed. s08 inserts a budgeting gate *before* the request goes out.

```diff
 // s01-s07: history grows forever; every turn (and every fat file block) stays.
-req := CreateMessageRequest{Messages: append(history, newTurn...)}
-resp, _ := provider.CreateMessage(ctx, req)   // eventually: 413 / context overflow

 // s08: budget-check and trim BEFORE sending.
+req := CreateMessageRequest{Messages: append(history, newTurn...)}
+cm := NewContextManager(budget, keepRecent)
+req.Messages = cm.truncate(req)               // drop stale middle if over budget
+resp, _ := provider.CreateMessage(ctx, req)   // request now fits the window
```

Semantically: s07 made each turn potentially *huge* (whole-file diff results), which is exactly what makes the window fill up — so s08 is its natural counterweight. The `Message` type is unchanged; what's new is a pass over the `[]Message` list that prunes the middle while protecting the two ends and the tool-call pairing. Note one deliberate gap: cline first tries to *shrink* the history (collapsing duplicate file reads) before truncating; s08 teaches the truncation step only.

## Try It

```bash
cd agents/s08-context-window-management

# Default: a 25-message history overflows a 3000-token budget → truncation runs.
go run .

# Huge budget → nothing to drop; history returned unchanged.
go run . -budget 100000

# Tight budget → one pass isn't enough (fat recent turns); still over after.
go run . -budget 1500

# Preserve more recent turns, or fabricate a longer conversation to drop from.
go run . -keep 8 -turns 30

# Tests: fully offline, no network, no LLM.
go test -v ./...
```

Expected output shape:

```
[s08] budget=3000 keepRecent=4 turns=12
  before: messages=25  est_tokens=13443  over_budget=true
  roles:  u a u a u a u a u a u a u a u a u a u a u a u a u
  drop range: [2..19] (18 messages, even=true, last-removed role="assistant")
  after:  messages=7  est_tokens=2328  over_budget=false
  roles:  u a u a u a u
  truncated=true
  invariants: first_task_msg_kept=true recent_tail_kept=true no_orphan_tool_result=true
```

The demo is deterministic (no LLM), so it matches `testdata/expected.txt` byte-for-byte. The `roles` lines make the invariant visible: the `after` timeline still opens with the kept `u a` pair and ends with the recent tail.

## Upstream Source Reading

cline's equivalent lives in `apps/vscode/src/core/context/context-management/ContextManager.ts`. `getNewContextMessagesAndMetadata` (L227) reads the previous request's real token usage from the saved `ClineApiReqInfo`, compares it to `getContextWindowInfo(api).maxAllowedSize`, and picks `keep = "half"` or `"quarter"` (the quarter case handles switching to a smaller-window model, where halving isn't enough). It then calls `getNextTruncationRange` (L299, below) for the index range and `getAndAlterTruncatedMessages` (L344) to build the trimmed list. The main difference from our port: cline triggers on *measured* usage and persists per-message edits to disk for checkpointing; we estimate the outgoing request and keep everything in memory.

```upstream:apps/vscode/src/core/context/context-management/ContextManager.ts#L299-L339
// Source: ContextManager.ts getNextTruncationRange (simplified)
// Returns an INCLUSIVE [start, end] range of message indices to remove.
public getNextTruncationRange(
	apiMessages: Anthropic.Messages.MessageParam[],
	currentDeletedRange: [number, number] | undefined,
	keep: "none" | "lastTwo" | "half" | "quarter",
): [number, number] {
	// Always keep the first user-assistant pair; truncate from index 2 onward.
	const rangeStartIndex = 2
	const startOfRest = currentDeletedRange ? currentDeletedRange[1] + 1 : 2

	let messagesToRemove: number
	if (keep === "half") {
		// Half of remaining pairs: /4 then *2 keeps the result EVEN (whole pairs).
		messagesToRemove = Math.floor((apiMessages.length - startOfRest) / 4) * 2
	} else {
		// "quarter": remove 3/4 — needed when moving to a smaller-window model
		// (e.g. claude 200k -> deepseek 64k, where halving isn't enough).
		messagesToRemove = Math.floor(((apiMessages.length - startOfRest) * 3) / 4 / 2) * 2
	}

	let rangeEndIndex = startOfRest + messagesToRemove - 1

	// The last removed message must be an assistant message, so the message AFTER
	// the kept pair is a user message — preserving user-assistant-user-assistant.
	if (apiMessages[rangeEndIndex] && apiMessages[rangeEndIndex].role !== "assistant") {
		rangeEndIndex -= 1 // can flip the removed count to odd — that's acceptable
	}

	return [rangeStartIndex, rangeEndIndex] // inclusive range to remove
}
```

**Reading notes**:

- **`keep` is a strategy, not a count.** Upstream chooses `"half"` normally and `"quarter"` when even halving leaves the request over a (possibly newly-smaller) window. Our port has a single strategy — drop the whole stale middle down to `KeepRecent` — which is the same idea with one knob instead of an enum.
- **The even-count math, then the parity-breaking guard.** Upstream computes `messagesToRemove` as even (counting pairs), then the `role !== "assistant"` check can subtract one. We reproduce both, and our tests assert the *boundary role*, not the parity — exactly because the guard can make it odd.
- **Orphan stripping lives in a sibling method.** Upstream removes orphaned `tool_result`s in `applyContextHistoryUpdates` (L482-505) and further repairs pairing in `ensureToolResultsFollowToolUse` (L375). We fold the essential strip into `applyTruncation`.
- **We estimate; upstream measures.** cline reads `tokensIn + tokensOut + cacheWrites + cacheReads` from the last `api_req_started` message. With no live usage, our `estimateTokens` serializes the request and divides by 4 — a stand-in the chapter is explicit about.
- **One deliberate omission.** Before truncating, upstream runs `attemptFileReadOptimization` (L626): collapsing duplicate file reads to a "[duplicate]" notice can free >=30% and *skip* truncation. We teach truncation only; that optimization is the next layer.

**Read further**: start at `ContextManager.ts` → `getNewContextMessagesAndMetadata` (L227), follow `getNextTruncationRange` (L299) into `getAndAlterTruncatedMessages` (L344), then `applyContextHistoryUpdates` (L482) and `ensureToolResultsFollowToolUse` (L375). That trace is the real-source map for s08 — the full path from "the previous request was too big" to "here is a valid, trimmed message list".

---

**Next**: s09 extends the tool registry at runtime by loading tools from external **MCP** servers — adding capability rather than pruning context, the complementary axis to s08.
