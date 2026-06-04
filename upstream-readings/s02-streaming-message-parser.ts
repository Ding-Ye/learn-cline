// Source: apps/vscode/src/core/assistant-message/parse-assistant-message.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/parse-assistant-message.ts#L51-L135
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/parse-assistant-message.ts#L212-L240
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// This is cline's STREAMING assistant-message parser, parseAssistantMessageV2.
// Generic (non-native-tool-calling) models emit tool calls as XML-ish text
// inside their prose:
//
//     <write_to_file><path>a.txt</path><content>hi</content></write_to_file>
//
// cline calls parseAssistantMessageV2 on the GROWING accumulated string after
// every streamed delta, so it must (a) recognize tags the instant their last
// char arrives, and (b) report the trailing block as `partial` when the stream
// cut mid-tag. That partial flag is what lets the UI act before the bytes finish.
//
// The function is an index-driven state machine with three states: in-text,
// in-tool, in-param. The docs/{en,zh} excerpt shows the IN-TEXT → tool-start
// transition (L137-210). This file shows the OTHER two states (param-close and
// tool-close, L51-135) plus the finalization that flags partials (L212-240).
//
// s02's Go port (agents/s02-streaming-message-parser/parser.go) keeps the exact
// same three states; `endsWith(msg, i, tag)` is the Go form of upstream's
// `startsWith(tag, i - tag.length + 1)`.
// ----------------------------------------------------------------------------

const len = assistantMessage.length
for (let i = 0; i < len; i++) {
	const currentCharIndex = i

	// --- State: Parsing a Tool Parameter --- (we are inside <param>…)
	if (currentToolUse && currentParamName) {
		const closeTag = `</${currentParamName}>`
		// Does the closing tag END exactly at index i? (offset startsWith)
		if (
			currentCharIndex >= closeTag.length - 1 &&
			assistantMessage.startsWith(closeTag, currentCharIndex - closeTag.length + 1)
		) {
			// Slice the value between the opening and closing param tags.
			const value = assistantMessage
				.slice(currentParamValueStart, currentCharIndex - closeTag.length + 1)
				.trim()
			currentToolUse.params[currentParamName] = value
			currentParamName = undefined // back to tool-content state
			// NOTE: no `continue` — index i may ALSO close the tool or start a param.
		} else {
			continue // still inside the param value
		}
	}

	// --- State: Parsing a Tool Use (but not a specific parameter) ---
	if (currentToolUse && !currentParamName) {
		// Starting a new <param>? Scan the param-tag table.
		let startedNewParam = false
		for (const [tag, paramName] of toolParamOpenTags.entries()) {
			if (currentCharIndex >= tag.length - 1 && assistantMessage.startsWith(tag, currentCharIndex - tag.length + 1)) {
				currentParamName = paramName
				currentParamValueStart = currentCharIndex + 1 // value starts after the tag
				startedNewParam = true
				break
			}
		}
		if (startedNewParam) {
			continue
		}

		// Closing the current tool?
		const toolCloseTag = `</${currentToolUse.name}>`
		if (
			currentCharIndex >= toolCloseTag.length - 1 &&
			assistantMessage.startsWith(toolCloseTag, currentCharIndex - toolCloseTag.length + 1)
		) {
			// The slice between the tool's opening and closing tags = its body.
			const toolContentSlice = assistantMessage.slice(
				currentToolUseStart,
				currentCharIndex - toolCloseTag.length + 1,
			)

			// SPECIAL CASE: a write_to_file <content> can contain text that LOOKS
			// like a closing tag (it's a file body!). If the param scan above
			// missed </content>, recover it with lastIndexOf so nested tag-like
			// text survives. (s02 copies this exactly.)
			const contentParamName: ToolParamName = "content"
			if (
				currentToolUse.name === "write_to_file" /* || new_rule */ &&
				toolContentSlice.includes(`<${contentParamName}>`)
			) {
				const contentStartTag = `<${contentParamName}>`
				const contentEndTag = `</${contentParamName}>`
				const contentStart = toolContentSlice.indexOf(contentStartTag)
				const contentEnd = toolContentSlice.lastIndexOf(contentEndTag) // robust vs nesting
				if (contentStart !== -1 && contentEnd !== -1 && contentEnd > contentStart) {
					const contentValue = toolContentSlice.slice(contentStart + contentStartTag.length, contentEnd).trim()
					currentToolUse.params[contentParamName] = contentValue
				}
			}

			currentToolUse.partial = false // closing tag seen → COMPLETE
			contentBlocks.push(currentToolUse)
			currentToolUse = undefined
			currentTextContentStart = currentCharIndex + 1 // text resumes after the tag
			continue
		}
		// Otherwise still inside the tool body — keep scanning.
		continue
	}

	// --- State: Parsing Text / Looking for Tool Start ---
	// (shown in docs/{en,zh}/s02-streaming-message-parser.md, L137-210)
	// ...
}

// ----------------------------------------------------------------------------
// FINALIZATION (after the loop) — this is where `partial` is set.
// ----------------------------------------------------------------------------

// An open <param> inside an open tool: take the REST of the buffer as its value.
if (currentToolUse && currentParamName) {
	currentToolUse.params[currentParamName] = assistantMessage
		.slice(currentParamValueStart) // param start → end of string
		.trim()
	// The tool use REMAINS partial (its closing tag never arrived).
}

// An open tool use (maybe holding the finalized partial param above):
if (currentToolUse) {
	// Partial because the loop finished before the tool's closing tag.
	contentBlocks.push(currentToolUse)
}
// Trailing text (only if no tool was open at the very end):
else if (currentTextContent) {
	currentTextContent.content = assistantMessage
		.slice(currentTextContentStart) // text start → end of string
		.trim()
	// Text is partial because the loop finished here (more may stream in).
	if (currentTextContent.content.length > 0) {
		contentBlocks.push(currentTextContent)
	}
}

return contentBlocks

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start at parseAssistantMessageV2 (parse-assistant-message.ts L28). Read the
// three states in source order:
//
//   L51-75   in-param  : detect </param>, slice the value          (this file)
//   L77-135  in-tool   : detect <param> / </tool>, write_to_file fix(this file)
//   L137-210 in-text   : detect <tool>, close the text run         (docs excerpt)
//   L212-240 finalize  : flush the open block, set `partial`        (this file)
//
// The emitted union types live in assistant-message/index.ts:
//   ToolUse / TextStreamContent (L3-81) and the toolParamNames table (L13-59).
//
// Then follow a parsed ToolUse forward:
//   parse-assistant-message.ts  parseAssistantMessageV2   (this chapter, s02)
//     └─▶ task/ToolExecutor.ts  executeTool               → s03 (run the tool)
//           └─▶ task/tools/autoApprove.ts shouldAutoApproveTool → s04 (gate it)
//   and sideways to the NATIVE path that skips this parser entirely:
//     └─▶ api/providers/anthropic.ts createMessage        → s05 (streaming SSE)
//
// That trace is the real-source map for s02 → s03 → s04 → s05.
