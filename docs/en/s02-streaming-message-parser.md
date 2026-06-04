---
title: "s02 · Streaming Message Parser"
chapter: 2
slug: s02-streaming-message-parser
est_read_min: 11
---

# s02 · Streaming Message Parser

> What this teaches: how cline pulls tool calls out of the model's *text* stream as it arrives — an incremental parser for XML-ish `<tool><param>…</param></tool>` blocks that handles partial tags across chunk boundaries. It gets its own chapter because this is what lets cline stream and *act* before the full message lands.

---

## Problem

In s01 the model spoke Anthropic's **native** `tool_use` block: the API itself returned each tool call as a clean JSON object, already separated from the prose. That is the easy path, and not every model offers it. cline supports ~40 providers, and many models (anything without native tool calling) instead ask to run a tool by *writing XML in the middle of their normal output* — `<write_to_file><path>…</path><content>…</content></write_to_file>`.

Two things make that hard. First, the response **streams**: it arrives a few tokens at a time, so at any instant cline holds only a prefix of the message — maybe `<write_` with the rest still in flight. Second, cline wants to react *early*: show the prose live and, the moment a tool's tags are complete, hand the call off for approval. A parser that only runs on the finished message would throw away the entire point of streaming. We need a parser that consumes chunks, re-derives the block list each time, and can say "there's a tool call here, but it's not finished yet."

## Solution

The mental model: **the parser is a pure function of the accumulated buffer, re-run after every chunk.** `feed(chunk)` only appends to a buffer; `parse()` walks the *whole* buffer and returns the current block list, marking the trailing block `partial` if the buffer ends mid-block.

Why re-parse the whole buffer instead of resuming from saved state? Because a tag can be split *anywhere* — `<write_` + `to_file>`. If the parser tried to resume mid-tag it would have to remember partial-tag state across calls. By always working on the concatenation, the split is simply invisible: the parser never sees `<write_`, only the eventual `<write_to_file>`. This is exactly how upstream `parseAssistantMessageV2` is used — cline calls it on the growing string after each delta.

Three decisions worth calling out:

1. **Index-driven, not char-accumulator.** We scan with an index `i` and, at each position, ask "does the substring *ending* at `i` match a known tag?" Content is sliced out only when a block completes. (V1 used a char-by-char accumulator; V2 dropped it for speed — we keep V2's shape.)
2. **A recognized-tool table decides what's a tool.** `<write_to_file>` starts a tool; `<thinking>` does not — it's not in the table, so it stays *text*. Unknown tags are never tool calls.
3. **`write_to_file` content is special-cased.** A file body can itself contain `</content>`-looking text. If the normal param scan trips on it, we recover the value with `lastIndexOf("</content>")`, so the real closing tag wins.

## How It Works

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│  feed("<write_")  feed("to_file>")  feed("<path>a</path>…")   │
│        │               │                   │                  │
│        ▼               ▼                   ▼                  │
│   ┌──────────────────────────────────────────────┐          │
│   │   accumulated buffer (the whole message-so-far)│          │
│   └───────────────────────┬────────────────────────┘         │
│                           │ parse()  (index scan, 3 states)   │
│                           ▼                                    │
│        ┌─────────────┬─────────────┬──────────────┐          │
│        │  in-text    │  in-tool    │  in-param    │  states  │
│        └─────────────┴─────────────┴──────────────┘          │
│                           │                                    │
│                           ▼                                    │
│   []AssistantBlock:  text │ tool_use{name,params,partial}     │
│                                  └ partial=true if cut mid-tag │
└──────────────────────────────────────────────────────────────┘
```

The load-bearing core of the scan (excerpt from [`agents/s02-streaming-message-parser/parser.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s02-streaming-message-parser/parser.go)):

```go
n := len(msg)
for i := 0; i < n; i++ {
	// --- State: inside a tool PARAMETER ---
	if tool != nil && paramName != "" {
		closeTag := "</" + paramName + ">"
		if endsWith(msg, i, closeTag) {
			value := strings.TrimSpace(msg[paramValueStart : i-len(closeTag)+1])
			tool.Params[paramName] = value
			paramName = "" // back to tool-content state; fall through
		} else {
			continue // still inside the param value
		}
	}

	// --- State: inside a TOOL but not a specific param ---
	if tool != nil && paramName == "" {
		started := false
		for _, name := range toolParamNames { // starting a new <param>?
			if endsWith(msg, i, "<"+name+">") {
				paramName, paramValueStart, started = name, i+1, true
				break
			}
		}
		if started {
			continue
		}
		if endsWith(msg, i, "</"+tool.ToolName+">") { // closing the tool?
			tool.Partial = false // closing tag seen → complete
			blocks = append(blocks, *tool)
			tool, textStart = nil, i+1
			continue
		}
		continue // still inside the tool body
	}

	// --- State: TEXT / looking for a tool to start ---
	started := false
	for _, name := range recognizedTools {
		if endsWith(msg, i, "<"+name+">") {
			if inText { // flush the text run that ended where the tag began
				if c := strings.TrimSpace(msg[textStart : i-len(name)-2+1]); c != "" {
					blocks = append(blocks, AssistantBlock{Type: "text", Text: c})
				}
				inText = false
			}
			tool = &AssistantBlock{Type: "tool_use", ToolName: name,
				Params: map[string]string{}, Partial: true} // partial until closed
			toolContentStart, started = i+1, true
			break
		}
	}
	if started {
		continue
	}
	if !inText {
		textStart, inText = i, true // a fresh text run begins
	}
}
```

**Four non-obvious points**:

1. **A new tool starts as `partial: true`.** It only flips to `false` when its closing `</tool>` is read. So if the stream ends first, the block is correctly reported as in-progress — which is what lets the UI render "about to write a file…".
2. **`endsWith(msg, i, tag)` is the whole trick.** It asks "does `tag` end exactly at index `i`?" (the Go form of upstream's `startsWith(tag, i-len+1)`). A single forward pass can thus recognize both opening and closing tags the instant their last byte arrives.
3. **Text that ends because a tool began is *not* partial; trailing text *is*.** When a `<tool>` tag closes a text run, that text is final. But text at the very end of the buffer is marked partial — more prose might still stream in. (Mirrors upstream's finalization branch.)
4. **The recognized-tool and param tables are the grammar.** Anything not in them is literal text, so a model that writes `<thinking>` or invents `<not_a_tool>` produces a plain text block, never a phantom tool call.

## What Changed

s01's assistant response was **atomic and pre-structured** — `resp.Content` already held `tool_use` blocks with parsed `Input` maps, handed over by the provider. s02 takes the opposite input: a *string that grows over a stream*, and reconstructs the blocks itself.

```diff
-// s01: the provider already split tool calls into structured JSON.
-type ContentBlock struct {
-	Type  string                 // "text" | "tool_use" | "tool_result"
-	Name  string                 // tool name, given to us by the API
-	Input map[string]interface{} // tool args, parsed by the API
-}
-// loop reads resp.Content directly — no parsing needed.
+// s02: tool calls arrive as XML text in the STREAM; we parse them out.
+type AssistantBlock struct {
+	Type     string            // "text" | "tool_use"
+	ToolName string            // parsed from <tool_name> … </tool_name>
+	Params   map[string]string // parsed from <param> … </param> pairs
+	Partial  bool              // true while the closing tag hasn't arrived
+}
+
+type StreamingParser struct{ buf strings.Builder }
+func (p *StreamingParser) feed(chunk string)  { p.buf.WriteString(chunk) }
+func (p *StreamingParser) parse() []AssistantBlock { /* index scan */ }
```

The semantic shift: in s01 "what tool did the model call?" was answered by the API; in s02 it is answered by *our* state machine reading raw text, incrementally, while the bytes are still arriving. The `Partial` flag is brand new — it has no meaning when responses are atomic, and is the core of streaming.

## Try It

Everything here is offline and deterministic — there is no LLM in this chapter, just a hard-coded sample assistant turn fed in chunks.

```bash
cd agents/s02-streaming-message-parser

# Feed the sample in 7-byte chunks (splits tags across boundaries on purpose)
go run .

# Watch the block list grow after each chunk (partial tool → complete)
go run . -steps

# Feed it all at once — the final parse is byte-identical to the chunked run
go run . -chunk 0

# Run the 8 tests
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape (this one *is* reproducible — no model in the loop):

```
[s02] feeding 39 chunk(s) of a streamed assistant message
=== final parse ===
  [0] text: "I'll create a small greeter for you."
  [1] tool_use: write_to_file
        path = "greet.go"
        content = "package main\n\nimport \"fmt\"\n\n// prints a greeting. ..."
  [2] text (partial): "That file is ready."
```

You ran it right if block `[1]` is a `tool_use` named `write_to_file` with **both** `path` and `content` extracted (despite the 7-byte chunking splitting the tags), and the trailing block `[2]` is text flagged `(partial)`. Run with `-steps` to watch `[1]` appear as partial and then complete once `</write_to_file>` arrives.

## Upstream Source Reading

cline's parser lives in `apps/vscode/src/core/assistant-message/parse-assistant-message.ts`; the whole function is `parseAssistantMessageV2` (L28-240). The union types it emits — `TextStreamContent` and `ToolUse` — are in `assistant-message/index.ts` (L3-81), along with the `toolParamNames` table. Here is the text/tool-start state through the end of the loop, the part that shows how a text run is closed when a tool begins and how a new tool is opened as `partial`:

```upstream:apps/vscode/src/core/assistant-message/parse-assistant-message.ts#L137-L210
// --- State: Parsing Text / Looking for Tool Start ---
if (!currentToolUse) {
	// Check if starting a new tool use
	let startedNewTool = false
	for (const [tag, toolName] of toolUseOpenTags.entries()) {
		// `startsWith(tag, i - tag.length + 1)`: does `tag` END exactly at i?
		if (currentCharIndex >= tag.length - 1 && assistantMessage.startsWith(tag, currentCharIndex - tag.length + 1)) {
			// End current text block if one was active (it ends where the tag began).
			if (currentTextContent) {
				currentTextContent.content = assistantMessage
					.slice(currentTextContentStart, currentCharIndex - tag.length + 1)
					.trim()
				currentTextContent.partial = false // ended because a tool started
				if (currentTextContent.content.length > 0) {
					contentBlocks.push(currentTextContent)
				}
				currentTextContent = undefined
			} else {
				// Any stray text between the last block and this tag.
				const potentialText = assistantMessage
					.slice(currentTextContentStart, currentCharIndex - tag.length + 1)
					.trim()
				if (potentialText.length > 0) {
					contentBlocks.push({ type: "text", content: potentialText, partial: false })
				}
			}

			// Start the new tool use — PARTIAL until its closing tag is found.
			currentToolUse = {
				type: "tool_use",
				name: toolName as ClineDefaultTool,
				params: {},
				partial: true,
				call_id: nanoid(8),
				isNativeToolCall: false,
			}
			currentToolUseStart = currentCharIndex + 1 // content starts after the tag
			startedNewTool = true
			break
		}
	}
	if (startedNewTool) {
		continue
	}

	// Not a tool tag → it's text. Open a text block if we aren't in one.
	if (!currentTextContent) {
		currentTextContentStart = currentCharIndex
		currentTextContent = { type: "text", content: "", partial: true }
	}
	// Content is extracted later (on tool start or at finalization).
}
```

**Reading notes**:

- **`startsWith(tag, i - tag.length + 1)` ⇄ our `endsWith(msg, i, tag)`.** Identical idea: "does this tag end at index `i`?" TypeScript checks a prefix at an offset; Go slices and compares. Both let one forward pass catch a tag the instant its last byte lands — the key to streaming.
- **`partial: true` on tool open.** Upstream sets it the same way we do, and only the closing-tag branch (L127, `currentToolUse.partial = false`) flips it. The finalization block at the bottom (L223-226) pushes any still-open tool *without* clearing partial — that is how a cut-off stream reports an in-progress tool.
- **`call_id: nanoid(8)` we drop.** Upstream tags each tool use with an id (used later to match streaming UI updates and native tool results). s02 has no UI and no native path here, so it is omitted; s03/s05 reintroduce identity when it earns its keep.
- **The `write_to_file` / `content` special case** (upstream L107-125, our `lastIndexOf("</content>")`). A file body can contain text that looks like a closing tag; upstream recovers `content` with `indexOf`/`lastIndexOf` on the tool's inner slice. We copy this exactly — it is the one place a naive tag scan is wrong.
- **A correct-but-imperfect choice we keep.** Re-parsing the entire buffer on every `parse()` is O(n) per call, so O(n²) over a full stream. Upstream accepts this too (assistant messages are small); a resumable parser would be faster but would have to carry partial-tag state, defeating the simplicity that makes partial handling obviously correct.

**Read further**: start at `parse-assistant-message.ts` → `parseAssistantMessageV2` (L28), read the param-close and tool-close states (L51-135), then the finalization block (L212-240); cross-reference the `ToolUse` / `toolParamNames` definitions in `assistant-message/index.ts` (L13-81). That trace is the real-source map for s02 → s03 (executing the parsed `tool_use`) → s05 (the native-tool-call stream that bypasses this parser).

---

**Next**: s03 takes the `tool_use` blocks this parser emits and *runs* them — a name-dispatched tool registry (`ToolExecutor`) that looks up a handler, validates params, executes, and wraps the output as a `tool_result` block to feed back into the s01 loop.
