// Source: apps/vscode/src/core/assistant-message/diff.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/diff.ts#L305-L393
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// The core of cline's file-edit-by-diff: the V1 constructNewFileContent loop.
// It walks the diff line-by-line, recognizing three markers, and on each
// "=======" (SEARCH closed) it LOCATES the search text in the original and
// emits the untouched original up to that point. Replacement lines are then
// streamed straight into `result`, so a partial diff renders progressively.
//
// The Go port (agents/s07-file-edit-diff/diff.go constructNewFileContent) is a
// direct transliteration of this loop: same markers, same exact→line-trimmed
// match order, same lastProcessedIndex bookkeeping. We omit V1's out-of-order
// replacement path and the V2 NewFileContentConstructor (non-standard-line
// recovery), keeping the streaming SHAPE that teaches the mechanism.
// ----------------------------------------------------------------------------

// Markers (diff.ts L1-L3). cline also accepts flexible lengths + legacy
// <<<<<<< / >>>>>>> via regex (isSearchBlockStart etc.); we match the canonical
// fixed strings, which is what the prompt emits.
//   const SEARCH_BLOCK_START = "------- SEARCH"
//   const SEARCH_BLOCK_END   = "======="
//   const REPLACE_BLOCK_END  = "+++++++ REPLACE"

for (const line of lines) {
	if (isSearchBlockStart(line)) {
		// Open a SEARCH section; start buffering the text to find.
		inSearch = true
		currentSearchContent = ""
		currentReplaceContent = ""
		continue
	}

	if (isSearchBlockEnd(line)) {
		// SEARCH closed → REPLACE opens. Now resolve WHERE this block applies.
		inSearch = false
		inReplace = true

		if (!currentSearchContent) {
			// Empty SEARCH: new file (empty original) → insert at 0.
			// (V1 errors on empty-SEARCH-with-nonempty-file; V2 treats it as a
			//  whole-file replace. Our Go port follows V2's friendlier rule.)
			if (originalContent.length === 0) {
				searchMatchIndex = 0
				searchEndIndex = 0
			} else {
				throw new Error("Empty SEARCH block detected with non-empty file. ...")
			}
		} else {
			// 1) EXACT match, forward from the last applied edit.
			const exactIndex = originalContent.indexOf(currentSearchContent, lastProcessedIndex)
			if (exactIndex !== -1) {
				searchMatchIndex = exactIndex
				searchEndIndex = exactIndex + currentSearchContent.length
			} else {
				// 2) LINE-TRIMMED fallback: compare lines ignoring per-line
				//    whitespace, but splice the byte-exact original span.
				const lineMatch = lineTrimmedFallbackMatch(originalContent, currentSearchContent, lastProcessedIndex)
				if (lineMatch) {
					;[searchMatchIndex, searchEndIndex] = lineMatch
				} else {
					// 3) BLOCK-ANCHOR fallback for 3+ line blocks (first/last line
					//    as anchors) — summarized in our port, not reimplemented.
					const blockMatch = blockAnchorFallbackMatch(originalContent, currentSearchContent, lastProcessedIndex)
					if (blockMatch) {
						;[searchMatchIndex, searchEndIndex] = blockMatch
					} else {
						throw new Error(`The SEARCH block:\n${currentSearchContent.trimEnd()}\n...does not match anything in the file.`)
					}
				}
			}
		}

		// Emit the original content between the previous edit and this match, so
		// `result` tracks the original verbatim right up to the spliced region.
		result += originalContent.slice(lastProcessedIndex, searchMatchIndex)
		continue
	}
	// ... isReplaceBlockEnd advances lastProcessedIndex = searchEndIndex and resets;
	// content lines accumulate into currentSearchContent (inSearch) or stream into
	// result (inReplace). On isFinal, trailing original is appended. (diff.ts L395-491)
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start at constructNewFileContent (diff.ts L245, the version dispatcher), drop
// into constructNewFileContentV1 (L266) for the loop above, then read the two
// fallbacks lineTrimmedFallbackMatch (L51-L103) and blockAnchorFallbackMatch
// (L132-L185). For the streaming-hardened variant, read the V2 class
// NewFileContentConstructor (L499-L821) and constructNewFileContentV2 (L823).
//
// That trace is the real-source map for s07: parse markers → tiered match →
// splice → stream. The diff this consumes is produced by the model and parsed
// out of the assistant stream by s02's parser; the file it edits is read/written
// through the s03 tool registry behind s04's approval gate.
