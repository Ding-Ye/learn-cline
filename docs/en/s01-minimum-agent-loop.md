---
title: "s01 · Minimum Agent Loop"
chapter: 1
slug: s01-minimum-agent-loop
est_read_min: 9
---

# s01 · Minimum Agent Loop

> What this teaches: the agent loop itself — the `request → tool → result → repeat` cycle that turns a one-shot LLM call into something that can actually *do* work. It gets its own chapter because every later chapter is a refinement of this one loop.

---

## Problem

A raw LLM call is a dead end for agency. You send a prompt, you get one reply, and that's it. The moment the model says "to answer this I need to read `config.go`" or "let me run the tests", a bare API call simply stops — there is nobody to read the file, nobody to run the tests, and no way to tell the model what happened.

cline's entire job is to close that gap. When the model emits a tool call, *something* must execute it, capture the output, hand it back to the model, and ask the model what to do next — over and over until the model is satisfied. That "something" is the agent loop. Before we build streaming, a tool registry, approval gating, or checkpoints, we have to build the loop they all hang from. This chapter builds the smallest version that genuinely deserves the name.

## Solution

The mental model is one sentence: **an agent is not the LLM call, it is the loop around it.**

Each turn does four things: (1) send the whole conversation plus the available tool schemas to the provider; (2) append the assistant's reply to history — *even when it's a tool call*, because the protocol requires the model to see its own prior `tool_use` before it can match the `tool_result`; (3) branch on `stop_reason` — if the model asked for tools, run them and append the results as the next *user* message; if it stopped, return its text; (4) loop, with a hard turn cap so a confused model can never spin forever.

Three decisions worth calling out:

1. **One internal block model.** We use the Anthropic native `tool_use` / `tool_result` shape everywhere; the OpenAI provider translates at its boundary. The loop never knows which provider answered.
2. **Tool results are a *user* turn.** This is the part that surprises people: the model's tool call lives in an *assistant* message, but the output goes back as a *user* message containing `tool_result` blocks. That alternation is the conversation protocol.
3. **`stop_reason` is the brain.** The loop doesn't parse the text to decide whether to continue — it reads `stop_reason`. `tool_use` means keep going; `end_turn` means done.

## How It Works

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                                                              │
│   userPrompt                                                 │
│       │                                                      │
│       ▼                                                      │
│   ┌───────────────────────┐                                 │
│   │ Provider.CreateMessage│◀──────────────┐                 │
│   └───────────┬───────────┘               │                 │
│               │ assistant turn            │ tool_result     │
│               ▼                           │ (user message)  │
│         stop_reason?                      │                 │
│          /        \                       │                 │
│   "tool_use"   "end_turn"          ┌──────┴──────┐          │
│       │             │              │  run tools  │          │
│       └────────────────────────▶  └─────────────┘          │
│                     │                                       │
│                     ▼                                       │
│                 final text  (also: MaxTurns cap aborts)     │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

The load-bearing 30-some lines (excerpt from [`agents/s01-minimum-agent-loop/loop.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s01-minimum-agent-loop/loop.go)):

```go
// The conversation starts with the user's prompt.
messages := []Message{
	{Role: "user", Content: []ContentBlock{{Type: "text", Text: userPrompt}}},
}

for turn := 0; turn < t.MaxTurns; turn++ {
	resp, err := t.Provider.CreateMessage(ctx, CreateMessageRequest{
		Messages: messages,
		Tools:    schemas,
	})
	if err != nil {
		return "", fmt.Errorf("turn %d: %w", turn, err)
	}

	// 1. Append the assistant turn — even if it contains tool_use blocks,
	// the protocol requires it in history so the next request's tool_result
	// has a matching tool_use to point at.
	messages = append(messages, Message{Role: "assistant", Content: resp.Content})

	// 2. stop_reason is the loop's brain: keep going vs. we're done.
	switch resp.StopReason {
	case "end_turn", "stop_sequence":
		return extractText(resp.Content), nil

	case "tool_use":
		// 3. Run every requested tool, feed the results back as ONE user message.
		toolResults := t.runTools(ctx, resp.Content, toolByName, turn)
		messages = append(messages, Message{Role: "user", Content: toolResults})

	case "max_tokens":
		return "", fmt.Errorf("hit max_tokens at turn %d (response was truncated)", turn)

	default:
		return "", fmt.Errorf("unexpected stop_reason %q at turn %d", resp.StopReason, turn)
	}
}
// 4. Turn cap reached — never loop forever.
return "", fmt.Errorf("loop exceeded MaxTurns=%d without end_turn", t.MaxTurns)
```

**Four non-obvious points**:

1. **The assistant turn is appended before the tools run** — if you skip it, the next request has `tool_result` blocks with no `tool_use` to reference, and the API rejects it.
2. **Tool results go back as a `user` message** — not assistant. One `tool_result` block per `tool_use`, each linked by `ToolUseID`.
3. **Errors become content, not control flow** — an unknown tool or a failing tool produces an *error* `tool_result` (with `IsError: true`) that the model can read and recover from, rather than crashing the loop.
4. **The turn cap is non-negotiable** — without it, a model that keeps calling tools (or two tools that ping-pong) loops until you kill the process.

## What Changed

This is the first chapter — it establishes the `Task` / `Provider` / `Message` vocabulary every later chapter reuses. There is no previous chapter to diff against; instead, here is the s01 baseline, the core types the rest of the curriculum builds on:

```go
// The wire shape (Anthropic Messages model) — our single internal vocabulary.
type ContentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"

	Text string `json:"text,omitempty"` // type == "text"

	ID    string                 `json:"id,omitempty"`    // type == "tool_use"
	Name  string                 `json:"name,omitempty"`  //  "
	Input map[string]interface{} `json:"input,omitempty"` //  "

	ToolUseID   string      `json:"tool_use_id,omitempty"` // type == "tool_result"
	ToolContent interface{} `json:"content,omitempty"`     //  "
	IsError     bool        `json:"is_error,omitempty"`    //  "
}

// The LLM call, abstracted so the loop / tests / later providers are swappable.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// One executable capability. The loop only ever sees this interface.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

// The agent loop. The smallest thing that earns the name.
type Task struct {
	Provider Provider
	Tools    []Tool
	MaxTurns int
}
```

## Try It

Tests are fully offline — no network, no API key (a scripted `fakeProvider` drives the loop):

```bash
cd agents/s01-minimum-agent-loop

# Run the 9 tests / see each case
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...

# Run the real agent (needs a key for your chosen provider)
export ANTHROPIC_API_KEY=sk-...
go run . -v "create hello.txt containing the text hi"

# Any OpenAI-compatible provider works too
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -v "run: echo hello from bash"
```

Expected output shape (LLM output is non-deterministic, so match the *shape*):

```
[s01] provider=anthropic model=claude-sonnet-4-6 url=
[turn 0] assistant: I'll create that file for you.
[turn 0] -> write_file map[content:hi path:hello.txt]
[turn 0] <- wrote 2 bytes to hello.txt
[turn 1] assistant: Done — hello.txt now contains "hi".
Done — hello.txt now contains "hi".
```

You ran it right if you see: a `-> tool` line, a matching `<- result` line, then a later turn with no tool call, then the final text on stdout. A `loop exceeded MaxTurns` error means the model kept calling tools without finishing — raise `-max-turns` or clarify the prompt.

## Upstream Source Reading

cline's loop lives in `apps/vscode/src/core/task/index.ts` (3,764 lines). The loop *driver* is `initiateTaskLoop`; it repeatedly calls `recursivelyMakeClineRequests`, which does one full turn (build request → stream → parse → run tools → append results) and returns `didEndLoop`. The whole "are we done?" decision is that one boolean. Here is the driver, lightly annotated:

```upstream:apps/vscode/src/core/task/index.ts#L1453-L1480
private async initiateTaskLoop(userContent: ClineContent[]): Promise<void> {
	let nextUserContent = userContent           // turn 0: the task; later: tool_results / a nudge
	let includeFileDetails = true               // workspace snapshot — only on the first request

	while (!this.taskState.abort) {             // run until cancelled or a turn ends the loop
		// ONE TURN: request → stream → parse → run tools → append tool_results.
		// Returns didEndLoop=true only when the model used no tools (i.e. stopped asking for work).
		const didEndLoop = await this.recursivelyMakeClineRequests(nextUserContent, includeFileDetails)
		includeFileDetails = false              // we only need file details the first time

		//  The way this agentic loop works is that cline will be given a task that he then calls
		//  tools to complete. unless there's an attempt_completion call, we keep responding back
		//  to him with his tool's responses until he either attempt_completion or does not use
		//  anymore tools. If he does not use anymore tools, we ask him to consider if he's
		//  completed the task and then call attempt_completion, otherwise proceed ...

		if (didEndLoop) {
			break
		}
		// Model returned only text but didn't finish: inject a "you used no tools" message and loop.
		nextUserContent = [
			{
				type: "text",
				text: formatResponse.noToolsUsed(this.useNativeToolCalls),
			},
		]
		this.taskState.consecutiveMistakeCount++
	}
}
```

**Reading notes**:

- **Two methods vs. our one.** cline separates the *driver* (`initiateTaskLoop`) from *one turn* (`recursivelyMakeClineRequests`) because the turn is a big streaming routine. s01 has no streaming, so `Task.Run` collapses both into a single for-loop — same shape, less ceremony.
- **`didEndLoop` vs. our `stop_reason`.** cline computes "done" inside the turn and returns a boolean; s01 reads `stop_reason == "end_turn"` directly. Same decision, different place.
- **The "no tools used" nudge.** cline refuses to treat "text but no `attempt_completion`" as done — it injects a synthetic user message and loops again. s01 simplifies: `end_turn` ends the loop. The explicit `attempt_completion` tool is taught in a later chapter.
- **Turn cap vs. mistake limit.** cline bounds runaways with `maxConsecutiveMistakes` and an API-request limit (and `taskState.abort` for cancellation). s01 uses a single hard `MaxTurns` — cruder, but it makes the "never loop forever" guarantee obvious.
- **Streaming, omitted on purpose.** Upstream `recursivelyMakeClineRequests` consumes an async stream and updates the UI live. s01 uses the buffered `CreateMessage` so the loop is the only thing you have to understand here. s02 (streaming XML parser) and s05 (provider streaming) add it back.

**Read further**: start at `index.ts` → `initiateTaskLoop` (L1453), follow the call into `recursivelyMakeClineRequests` (L2354), then branch out to `api/providers/anthropic.ts` `createMessage` and `assistant-message/parse-assistant-message.ts`. That trace is the real-source map for s01 → s02 → s03 → s05.

---

**Next**: s02 evolves this chapter's loop by replacing the atomic response with a *stream*, and teaches cline's custom XML assistant-message parser (`parseAssistantMessageV2`) that pulls `tool_use` blocks out of a growing string before the message is even finished.
