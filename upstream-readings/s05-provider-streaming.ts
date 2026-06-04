// Source: apps/vscode/src/core/api/providers/anthropic.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/api/providers/anthropic.ts#L181-L302
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// This is the heart of cline's provider-streaming abstraction: the loop that
// consumes the Anthropic SSE stream and YIELDS normalized ApiStreamChunks. Every
// provider (anthropic.ts, openai.ts, gemini.ts, ...) is an async generator that
// emits the SAME chunk union (api/transform/stream.ts ApiStreamChunk). The agent
// loop iterates that union and never learns which vendor answered.
//
// `createMessage` is an `async *generator` returning `ApiStream`. The Go port
// (agents/s05-provider-streaming/provider.go) returns a `<-chan StreamChunk`
// instead — Go's idiom for an async iterable — and our chunk tags
// (ChunkText / ChunkToolUseStart / ChunkToolUseDelta / ChunkUsage / ChunkDone)
// mirror this switch's cases one-for-one.
// ----------------------------------------------------------------------------

// The @anthropic-ai/sdk gives back an async-iterable of already-parsed SSE
// events. The SDK hides the raw `data:` line protocol; our Go version decodes
// those lines by hand (decodeAnthropicSSE) since stdlib has no Anthropic SDK.
// `lastStartedToolCall` carries the id+name forward, because the JSON-input
// deltas that follow do NOT repeat them — see input_json_delta below.
const lastStartedToolCall = { id: "", name: "", arguments: "" }

for await (const chunk of stream) {
	switch (chunk?.type) {
		case "message_start": {
			// Arrives FIRST and carries the input-token accounting (plus cache
			// read/write counts). cline yields a usage chunk immediately.
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
			// Periodic + final: the running output-token count (and stop_reason).
			yield { type: "usage", inputTokens: 0, outputTokens: chunk.usage.output_tokens || 0 }
			break
		case "message_stop":
			// Pure terminator — no payload. (Our Go port emits a ChunkDone here.)
			break
		case "content_block_start":
			switch (chunk.content_block.type) {
				case "tool_use":
					// A tool_use block OPENS with its id + name. Remember them;
					// the arguments stream separately as input_json_delta frames.
					if (chunk.content_block.id && chunk.content_block.name) {
						lastStartedToolCall.id = chunk.content_block.id
						lastStartedToolCall.name = chunk.content_block.name
						lastStartedToolCall.arguments = ""
					}
					break
				case "text":
					// Multiple text blocks get a newline between them.
					if (chunk.index > 0) {
						yield { type: "text", text: "\n" }
					}
					yield { type: "text", text: chunk.content_block.text }
					break
			}
			break
		case "content_block_delta":
			switch (chunk.delta.type) {
				case "text_delta":
					// The common case: a slice of assistant prose.
					yield { type: "text", text: chunk.delta.text }
					break
				case "input_json_delta":
					// PARTIAL tool-input JSON. Note it carries no id/name — cline
					// re-attaches lastStartedToolCall here. The consumer concatenates
					// every partial_json fragment and JSON.parses once at the end.
					if (lastStartedToolCall.id && lastStartedToolCall.name && chunk.delta.partial_json) {
						yield {
							type: "tool_calls",
							tool_call: {
								...lastStartedToolCall,
								function: {
									id: lastStartedToolCall.id,
									name: lastStartedToolCall.name,
									arguments: chunk.delta.partial_json,
								},
							},
						}
					}
					break
			}
			break
		case "content_block_stop":
			// One block finished; clear the in-flight tool so the NEXT tool_use
			// starts clean. (Our Go assembler keys deltas by the active id instead.)
			lastStartedToolCall.id = ""
			lastStartedToolCall.name = ""
			lastStartedToolCall.arguments = ""
			break
	}
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start here (anthropic.ts createMessage L64, the event switch L183-L302), then
// see how the SAME chunk union is produced by a different wire format:
//
//   api/providers/anthropic.ts createMessage  (this file)        ← native SSE
//     └─▶ api/transform/stream.ts ApiStreamChunk (L1-L70)        ← the union both emit
//           └─▶ api/providers/openai.ts createMessage (L97-L180) ← OpenAI delta SSE
//                 └─▶ api/index.ts buildApiHandler (L478) /
//                     createHandlerForProvider (L76)             ← the factory
//
// That trace is the real-source map for s05: one streamed union, many providers,
// chosen by a single factory. It feeds straight into s02's parser (which can now
// be driven by genuine text deltas instead of a pre-baked string).
