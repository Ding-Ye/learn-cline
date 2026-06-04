// Source: apps/vscode/src/core/context/context-management/ContextManager.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/context/context-management/ContextManager.ts#L299-L339
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// The truncation CORE of cline's context-window management. When the previous
// request's real token usage neared the model's window, getNewContextMessagesAnd-
// Metadata (L227) decides whether to keep "half" or a "quarter" of the remaining
// messages, then calls getNextTruncationRange (below) to compute the inclusive
// [start, end] index range to DELETE, and getAndAlterTruncatedMessages (L344) to
// produce the trimmed list.
//
// The Go port (agents/s08-context-window-management/context.go) collapses these
// into estimateTokens / nextTruncationRange / truncate. The two load-bearing
// ideas are reproduced here: (1) the range math that always keeps the first pair
// and removes an even count ending on an assistant message, and (2) the orphan
// tool_result strip that keeps the API-required tool_use/tool_result pairing valid.
// ----------------------------------------------------------------------------

// --- getNextTruncationRange (L299-339) -------------------------------------
// Returns an INCLUSIVE [start, end] range of message indices to remove.
public getNextTruncationRange(
	apiMessages: Anthropic.Messages.MessageParam[],
	currentDeletedRange: [number, number] | undefined,
	keep: "none" | "lastTwo" | "half" | "quarter",
): [number, number] {
	// We ALWAYS keep the first user-assistant pairing (the task definition) and
	// truncate an even number of messages from index 2 onward.
	const rangeStartIndex = 2 // index 0 and 1 are kept
	// If we've truncated before, start just past the prior deleted range.
	const startOfRest = currentDeletedRange ? currentDeletedRange[1] + 1 : 2

	let messagesToRemove: number
	if (keep === "half") {
		// Remove half of the remaining pairs: take a quarter of the message count
		// and double it, so the result is always EVEN (whole pairs).
		messagesToRemove = Math.floor((apiMessages.length - startOfRest) / 4) * 2
	} else {
		// keep === "quarter": switching to a smaller-window model may need 3/4 gone
		// (e.g. Claude 200k -> deepseek 64k, where halving isn't enough).
		messagesToRemove = Math.floor(((apiMessages.length - startOfRest) * 3) / 4 / 2) * 2
	}

	let rangeEndIndex = startOfRest + messagesToRemove - 1 // inclusive end

	// Make sure the LAST removed message is an assistant message, so the message
	// AFTER the kept pair is a user message. This preserves the strict
	// user-assistant-user-assistant structure cline relies on (anthropic format).
	if (apiMessages[rangeEndIndex] && apiMessages[rangeEndIndex].role !== "assistant") {
		rangeEndIndex -= 1 // NOTE: this can flip the removed COUNT to odd — that's fine
	}

	return [rangeStartIndex, rangeEndIndex] // inclusive range to remove
}

// --- applyContextHistoryUpdates (L482-505, excerpt) ------------------------
// After computing the range, the list is rebuilt as [first pair] + [tail], and
// the FIRST surviving tail message gets its orphaned tool_results stripped —
// otherwise the model receives a tool_result whose tool_use we just deleted.
const firstChunk = messages.slice(0, 2)            // kept first user/assistant pair
const secondChunk = messages.slice(startFromIndex) // everything past the deleted range
const messagesToUpdate = [...firstChunk, ...secondChunk]

if (startFromIndex > 2 && messagesToUpdate.length > 2) {
	const firstMessageAfterTruncation = messagesToUpdate[2]
	if (firstMessageAfterTruncation.role === "user" && Array.isArray(firstMessageAfterTruncation.content)) {
		const hasToolResults = firstMessageAfterTruncation.content.some((b) => b.type === "tool_result")
		if (hasToolResults) {
			// Clone (never mutate the original history) and drop every tool_result block.
			messagesToUpdate[2] = cloneDeep(firstMessageAfterTruncation)
			messagesToUpdate[2].content = firstMessageAfterTruncation.content.filter(
				(b) => b.type !== "tool_result",
			)
		}
	}
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start at getNewContextMessagesAndMetadata (L227): it reads the previous
// request's tokensIn/Out/cache from the saved ClineApiReqInfo, compares against
// getContextWindowInfo(api).maxAllowedSize, and picks keep = "half" | "quarter"
// (L249-252). Then:
//
//   getNewContextMessagesAndMetadata (L227)            ← the trigger + keep choice
//     └─▶ getNextTruncationRange (L299)                ← THIS range math
//           └─▶ getAndAlterTruncatedMessages (L344)    ← splice + alter
//                 └─▶ applyContextHistoryUpdates (L482) ← orphan tool_result strip
//                 └─▶ ensureToolResultsFollowToolUse (L375) ← repair any remaining pairing
//
// That trace is the real-source map for s08. The big thing we LEAVE OUT: cline
// also runs attemptFileReadOptimization first (L626) — replacing duplicate file
// reads with a "[duplicate]" notice can free enough space (>=30%) to SKIP
// truncation entirely. We teach truncation-only; condensing/optimization is the
// next layer up.
