---
title: "s05 · Provider Streaming Abstraction"
chapter: 5
slug: s05-provider-streaming
est_read_min: 13
---

# s05 · Provider Streaming Abstraction

> What this teaches: the **provider streaming abstraction** — how cline turns ~40 different LLM APIs into one async stream of normalized chunks behind a factory, so the agent loop is written once and the vendor is a single string you pick at startup.

---

## Problem

s01-s04 leaned on a fake, buffered provider: one request in, one whole `CreateMessageResponse` out. That was the right simplification for teaching the loop, the parser, the tool registry, and approval — but it is a lie about how real models behave. Real APIs **stream**: the model emits tokens as it thinks, over Server-Sent Events, and cline shows that text live and detects tool calls *before* the message is finished.

Two pains fall out. First, every vendor speaks a different wire dialect — Anthropic's `content_block_delta` is nothing like OpenAI's `choices[].delta.tool_calls`, which is nothing like Gemini's. If that difference leaks into the loop, you get 40 special cases scattered everywhere (an anti-pattern cline explicitly warns about). Second, you need to pick a provider at runtime without rewriting the loop. This chapter solves both: normalize every stream into one `StreamChunk` type, and put a `buildHandler` factory in front so the choice is one string.

## Solution

Model the provider as a generator of normalized events. cline's `createMessage` is an `async *generator` yielding an `ApiStreamChunk` union; Go's idiom for the same thing is a receive-only channel, so our `Provider.CreateMessageStream` returns `<-chan StreamChunk`.

Three design decisions carry the chapter:

1. **One chunk union, produced at the boundary.** `StreamChunk` has five tags — `text`, `tool_use_start`, `tool_use_delta`, `usage`, `done`. Each provider decodes its own SSE format into exactly these. The loop switches on the tag, never on a vendor.
2. **Streaming is primary; buffering is a fold over the stream.** A provider implements streaming once; the buffered `CreateMessage` (the s01-style convenience) is a shared `assembleResponse` that drains the channel and folds chunks back into one response. No vendor implements buffering twice.
3. **A factory selects the provider, and errors loudly on the unknown.** `buildHandler(name, cfg)` mirrors cline's `buildApiHandler` / `createHandlerForProvider` switch. The OpenAI-compatible aliases (deepseek, moonshot, qwen, groq, ...) all resolve to one `OpenAIProvider` with a different base URL.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  buildHandler("anthropic"|"deepseek"|...)  ─▶  Provider          │
│                                                                  │
│  POST stream:true ─▶ SSE bytes ─▶ decode<Vendor>SSE              │
│        anthropic: message_start / content_block_delta / stop     │
│        openai:    choices[].delta.content / .tool_calls / [DONE] │
│                          │                                       │
│                          ▼                                       │
│   <-chan StreamChunk : text · tool_use_start · tool_use_delta    │
│                        · usage · done                            │
│                          │                                       │
│                          ▼                                       │
│   assembleResponse ─▶ CreateMessageResponse (text + tool_use)    │
└────────────────────────────────────────────────────────────────┘
```

The core 40-line excerpt (from [`agents/s05-provider-streaming/provider.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s05-provider-streaming/provider.go)) — the Anthropic SSE decoder, the part that maps vendor events to the shared chunk union:

```go
func decodeAnthropicSSE(ctx context.Context, r io.Reader, out chan<- StreamChunk) {
	activeToolID := "" // input_json_delta frames don't repeat the id; remember it
	for ev := range sseEvents(r) {
		var p anthropicEvent
		if json.Unmarshal([]byte(ev.data), &p) != nil {
			continue // skip malformed lines
		}
		switch p.Type {
		case "message_start": // carries the input-token count up front
			if p.Message != nil {
				u := p.Message.Usage
				send(ctx, out, StreamChunk{Type: ChunkUsage,
					Usage: &Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}})
			}
		case "content_block_start": // a tool_use block opens with id + name
			if p.ContentBlock != nil && p.ContentBlock.Type == "tool_use" {
				activeToolID = p.ContentBlock.ID
				send(ctx, out, StreamChunk{Type: ChunkToolUseStart,
					ToolCallID: p.ContentBlock.ID, ToolName: p.ContentBlock.Name})
			}
		case "content_block_delta":
			switch p.Delta.Type {
			case "text_delta": // a slice of assistant prose
				send(ctx, out, StreamChunk{Type: ChunkText, Text: p.Delta.Text})
			case "input_json_delta": // a PARTIAL fragment of the tool's input JSON
				send(ctx, out, StreamChunk{Type: ChunkToolUseDelta,
					ToolCallID: activeToolID, InputJSON: p.Delta.PartialJSON})
			}
		case "message_stop":
			send(ctx, out, StreamChunk{Type: ChunkDone})
			return
		}
	}
	send(ctx, out, StreamChunk{Type: ChunkDone}) // terminate even if stop was missing
}
```

**Four non-obvious points**:

1. **`stream:true` is the whole switch.** The same `/v1/messages` endpoint returns one JSON blob without it and an SSE event stream with it. The provider sets it; nothing downstream changes.
2. **Tool input arrives as partial JSON, not a value.** `input_json_delta` frames carry `partial_json` string fragments like `{"path":` then `"main.go"}`. You concatenate every fragment and `json.Unmarshal` once at the end — never per-fragment.
3. **The delta frames don't repeat the tool id.** Anthropic sends the id+name once in `content_block_start`; we stash it in `activeToolID` and stamp it on each delta (cline does the same with `lastStartedToolCall`).
4. **Errors must surface before the channel.** A non-2xx returns an `error` from `CreateMessageStream` directly — the caller shouldn't have to drain a stream to discover a 401.

## What Changed (vs. s02)

In s02 the parser consumed a finished **string**: you handed `parseAssistantMessageV2` an accumulated buffer and it walked it. s05 changes where the bytes come from — they now arrive as a live stream of chunks off a real provider.

```diff
 // s02: the parser is fed an in-memory string built ahead of time.
-buf += delta            // someone already had the whole (or growing) text
-blocks := parser.Blocks(buf)

 // s05: a provider streams normalized chunks; text deltas ARE the feed.
+ch, err := provider.CreateMessageStream(ctx, req)   // <-chan StreamChunk
+for c := range ch {
+    switch c.Type {
+    case ChunkText:          // <- this is what you would feed s02's parser
+    case ChunkToolUseStart:  // native tool_use: id + name
+    case ChunkToolUseDelta:  // native tool_use: partial input JSON
+    case ChunkUsage, ChunkDone:
+    }
+}
```

Semantically: s02 owned parsing a *string*; s05 owns producing the *stream* that string would have come from. The two compose — s05's `ChunkText` deltas are exactly what s02's incremental parser wants to be fed. Note also that s05 surfaces the model's **native** `tool_use` events (id + name + partial JSON), the alternative to s02's XML tool-call path; cline supports both, and conflating them is a classic mistake.

## Try It

```bash
cd agents/s05-provider-streaming

# Offline: replay a recorded Anthropic SSE stream through the real decoder.
# No API key, fully deterministic — watch each normalized chunk arrive.
go run .

# Live: stream from a real provider (needs a key for your chosen profile).
export ANTHROPIC_API_KEY=sk-...
go run . -live -model claude-sonnet-4-20250514 -prompt "say hi in one line"

# Any OpenAI-compatible provider works through the same chunk pipeline.
export DEEPSEEK_API_KEY=sk-...
go run . -live -provider deepseek -model deepseek-chat -prompt "say hi"

# Tests: fully offline (fake strings.Reader + httptest), no network.
go test -v ./...
```

Expected output shape:

```
== offline demo: replaying a recorded Anthropic SSE stream ==
  [usage] in=42 out=1
  [text]  "Let me "
  [text]  "read that file."
  [tool]  start id=toolu_01 name=read_file
  [tool]  input += "{\"path\":"
  [tool]  input += "\"main.go\"}"
  [usage] in=0 out=17
  [done]

== assembled response ==
stop_reason: tool_use  usage: in=42 out=17
text: "Let me read that file."
tool_use: read_filemap[path:main.go]
```

Each `[...]` line is one `StreamChunk` in arrival order; the assembled response is what `assembleResponse` folds them into. Live runs differ token-by-token (LLM output is non-deterministic) but follow the same shape.

## Upstream Source Reading

cline's equivalent lives in `apps/vscode/src/core/api/providers/anthropic.ts`. `createMessage` (L64) is an `async *generator`; its `for await` event switch (L183-L302) maps each parsed SSE event to a chunk of the `ApiStreamChunk` union defined in `api/transform/stream.ts` (L1-L70). The factory that picks a provider is `buildApiHandler` (`api/index.ts` L478) calling the `createHandlerForProvider` switch (L76). The main difference: cline uses the `@anthropic-ai/sdk`, which parses the raw `data:` lines for it, so its loop iterates already-typed events; our Go port has no such SDK, so `decodeAnthropicSSE` decodes the SSE line protocol by hand.

```upstream:apps/vscode/src/core/api/providers/anthropic.ts#L183-L302
// Source: apps/vscode/src/core/api/providers/anthropic.ts (createMessage, simplified)
// The SDK yields already-parsed SSE events; cline maps each to an ApiStreamChunk.
const lastStartedToolCall = { id: "", name: "", arguments: "" }

for await (const chunk of stream) {
	switch (chunk?.type) {
		case "message_start": {
			// First event: input-token accounting (+ prompt-cache counts).
			const usage = chunk.message.usage
			yield {
				type: "usage",
				inputTokens: usage.input_tokens || 0,
				outputTokens: usage.output_tokens || 0,
				cacheWriteTokens: usage.cache_creation_input_tokens || undefined,
				cacheReadTokens: usage.cache_read_input_tokens || undefined,
			}
			break
		}
		case "message_delta":
			// Running output-token count (and stop_reason).
			yield { type: "usage", inputTokens: 0, outputTokens: chunk.usage.output_tokens || 0 }
			break
		case "content_block_start":
			switch (chunk.content_block.type) {
				case "tool_use":
					// A tool_use OPENS with id + name; arguments stream separately.
					if (chunk.content_block.id && chunk.content_block.name) {
						lastStartedToolCall.id = chunk.content_block.id
						lastStartedToolCall.name = chunk.content_block.name
					}
					break
				case "text":
					yield { type: "text", text: chunk.content_block.text }
					break
			}
			break
		case "content_block_delta":
			switch (chunk.delta.type) {
				case "text_delta":
					yield { type: "text", text: chunk.delta.text } // a prose slice
					break
				case "input_json_delta":
					// PARTIAL tool-input JSON — note it carries no id, so cline
					// re-attaches lastStartedToolCall. Concatenate, parse once.
					if (lastStartedToolCall.id && chunk.delta.partial_json) {
						yield {
							type: "tool_calls",
							tool_call: { ...lastStartedToolCall, function: {
								id: lastStartedToolCall.id,
								name: lastStartedToolCall.name,
								arguments: chunk.delta.partial_json,
							} },
						}
					}
					break
			}
			break
		case "content_block_stop":
			lastStartedToolCall.id = ""   // clear so the next tool_use starts clean
			lastStartedToolCall.name = ""
			break
	}
}
```

**Reading notes**:

- **The async generator → channel mapping.** Upstream `yield`s into an `ApiStream` (an `AsyncGenerator`); we `send` into a `<-chan StreamChunk`. Same contract: a lazily-produced, ordered sequence the consumer iterates.
- **We hand-roll SSE; upstream doesn't.** cline relies on `@anthropic-ai/sdk` to parse `data:` lines into typed events. Go stdlib has no Anthropic SDK, so `sseEvents` + `decodeAnthropicSSE` do that decoding (the bit the SDK hides).
- **Reasoning and cache fields are dropped on purpose.** Upstream also yields `reasoning` chunks (thinking blocks) and cache-token counts; s05 keeps only `text`/`tool_use`/`usage`/`done` to stay focused on the abstraction.
- **`content_block_stop` resets the in-flight tool.** Upstream clears `lastStartedToolCall` so a second tool_use in the same message starts clean; our assembler instead keys deltas by the active id, reaching the same result.
- **One deliberate imperfection.** If the stream ends without `message_stop` (a truncated fixture), we still emit a terminal `done` so consumers can always rely on it — "correct but lenient" rather than erroring.

**Read further**: start at `anthropic.ts` → `createMessage` (L64), follow the chunk shape into `api/transform/stream.ts` `ApiStreamChunk` (L1-L70), then see the same union produced by a different wire format in `api/providers/openai.ts` `createMessage`, and finally the factory in `api/index.ts` `buildApiHandler` (L478) → `createHandlerForProvider` (L76). That trace is the real-source map for s05 → s06 (the prompt that advertises tools) and s02 (the parser these deltas can now drive).

---

**Next**: s06 builds the modular, model-aware **system prompt** that tells the model which tools exist — the input side of the request whose streamed output s05 just learned to decode.
