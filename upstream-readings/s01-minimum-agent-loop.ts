// Source: apps/vscode/src/core/task/index.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts#L1453-L1480
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// This is THE agent loop of cline, distilled. cline's Task splits the loop into
// two methods:
//
//   initiateTaskLoop()             ← the driver (the `while` below)
//   recursivelyMakeClineRequests() ← one turn: build request, stream the model,
//                                     parse the assistant message, run tools,
//                                     and return `didEndLoop`.
//
// The driver keeps calling recursivelyMakeClineRequests until it returns
// didEndLoop=true. That single boolean is the whole "are we done?" decision.
//
// s01's Go `Task.Run` (agents/s01-minimum-agent-loop/loop.go) collapses these
// two methods into one for-loop, because without streaming there is no reason
// to separate "drive" from "one turn".
// ----------------------------------------------------------------------------

private async initiateTaskLoop(userContent: ClineContent[]): Promise<void> {
	// nextUserContent is what we send on the NEXT request. On turn 0 it is the
	// user's task; on later turns it becomes the tool_result blocks (assembled
	// inside recursivelyMakeClineRequests) or the "you used no tools" nudge below.
	let nextUserContent = userContent

	// File details (a snapshot of the workspace) are expensive, so cline only
	// includes them on the FIRST request and never again. s01 omits this entirely.
	let includeFileDetails = true

	// The loop runs until the task is aborted (cancellation) or until a turn
	// reports didEndLoop. There is no fixed iteration cap here — cline bounds
	// runaways with maxConsecutiveMistakes and an API-request limit instead.
	// s01 uses a simpler hard MaxTurns cap.
	while (!this.taskState.abort) {
		// ONE TURN. recursivelyMakeClineRequests:
		//   1. sends nextUserContent + history + tools to the model (streaming)
		//   2. parses the streamed assistant message into text + tool_use blocks
		//   3. asks for approval, executes approved tools
		//   4. appends each tool_result back into the conversation history
		//   5. returns didEndLoop=true ONLY when the model used no tools (or hit
		//      a terminal condition) — i.e. it stopped asking for work.
		const didEndLoop = await this.recursivelyMakeClineRequests(nextUserContent, includeFileDetails)
		includeFileDetails = false // we only need file details the first time

		// The upstream comment that explains the whole design, verbatim:
		//
		//   "The way this agentic loop works is that cline will be given a task
		//    that he then calls tools to complete. unless there's an
		//    attempt_completion call, we keep responding back to him with his
		//    tool's responses until he either attempt_completion or does not use
		//    anymore tools. If he does not use anymore tools, we ask him to
		//    consider if he's completed the task and then call attempt_completion,
		//    otherwise proceed with completing the task."

		if (didEndLoop) {
			break
		}

		// The model returned ONLY text and did not call any tool — but it also
		// didn't call attempt_completion. cline does NOT accept that as "done":
		// it injects a synthetic user message telling the model it used no tools,
		// and loops again. This is what forces the model to either keep working
		// or explicitly finish. (s01 treats end_turn as done; the explicit
		// attempt_completion tool is taught in a later chapter.)
		nextUserContent = [
			{
				type: "text",
				text: formatResponse.noToolsUsed(this.useNativeToolCalls),
			},
		]
		this.taskState.consecutiveMistakeCount++
	}
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start here (initiateTaskLoop, index.ts L1453), then follow the call into
// recursivelyMakeClineRequests (index.ts L2354) to see how ONE turn is built:
// the request assembly, the streamed parse, the tool execution, and how
// tool_result blocks are appended before the loop comes back around.
//
//   index.ts initiateTaskLoop  (the driver — this file)
//     └─▶ index.ts recursivelyMakeClineRequests (one turn, L2354)
//           ├─▶ api/providers/anthropic.ts createMessage  → s05 (streaming)
//           ├─▶ assistant-message/parse-assistant-message.ts → s02 (XML parser)
//           └─▶ task/ToolExecutor.ts executeTool          → s03 / s04 (tools + approval)
//
// That trace is the real-source map for s01 → s02 → s03 → s05.
