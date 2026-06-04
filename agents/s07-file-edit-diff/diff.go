package main

// diff.go — apply a SEARCH/REPLACE diff to file content.
//
// This is a Go port of cline's constructNewFileContent
// (apps/vscode/src/core/assistant-message/diff.ts). It backs the replace_in_file
// tool: instead of rewriting a whole file, the model emits surgical blocks and we
// splice each REPLACE over the span its SEARCH text locates in the original.
//
// The diff format uses three line markers (cline's exact byte strings):
//
//	------- SEARCH
//	[exact text to find in the original file]
//	=======
//	[text to replace it with]
//	+++++++ REPLACE
//
// Multiple blocks are applied in order. Because the applier walks the original
// forward via lastProcessedIndex, blocks MUST appear in the order they occur in
// the file. The function is streaming-aware: feed it partial diff chunks with
// isFinal=false as they arrive, and a final chunk with isFinal=true; the result
// grows monotonically so a UI can show the edit live.

import (
	"fmt"
	"strings"
)

// Marker byte strings — copied verbatim from cline (diff.ts L1-L3). cline also
// accepts flexible counts (3+ dashes/equals/pluses) and a legacy <<<<<<< / >>>>>>>
// style via regex; we recognize the canonical fixed markers, which is all the
// current prompt emits. See matchSearchStart / matchSearchEnd / matchReplaceEnd.
const (
	searchBlockStart = "------- SEARCH"
	searchBlockEnd   = "======="
	replaceBlockEnd  = "+++++++ REPLACE"
)

func matchSearchStart(line string) bool { return line == searchBlockStart }
func matchSearchEnd(line string) bool   { return line == searchBlockEnd }
func matchReplaceEnd(line string) bool  { return line == replaceBlockEnd }

// looksLikePartialMarker reports whether a line begins with a marker rune but is
// not a complete marker — i.e. it might be a marker that got cut off mid-stream.
// We drop such a trailing line so a half-arrived "----" can't corrupt output.
func looksLikePartialMarker(line string) bool {
	if matchSearchStart(line) || matchSearchEnd(line) || matchReplaceEnd(line) {
		return false
	}
	return strings.HasPrefix(line, "-") ||
		strings.HasPrefix(line, "=") ||
		strings.HasPrefix(line, "+") ||
		strings.HasPrefix(line, "<") ||
		strings.HasPrefix(line, ">")
}

// constructNewFileContent reconstructs a file by applying a streamed SEARCH/REPLACE
// diff to originalContent. Call it once with the whole diff and isFinal=true, or
// repeatedly with growing prefixes (isFinal=false) then a final isFinal=true call.
//
// Matching strategy per SEARCH block, in order (mirrors diff.ts L207-L211):
//  1. exact substring match from lastProcessedIndex forward;
//  2. line-trimmed match (ignore leading/trailing whitespace per line);
//  3. (cline also has a block-anchor fallback for 3+ line blocks — summarized,
//     not ported here, to keep the chapter focused; see lineTrimmedFallbackMatch).
//
// Empty SEARCH is special: with an empty original it CREATES the file (pure
// insertion / append); with a non-empty original it replaces the ENTIRE file.
func constructNewFileContent(diffContent, originalContent string, isFinal bool) (string, error) {
	var result strings.Builder
	lastProcessedIndex := 0

	var currentSearch strings.Builder
	inSearch := false
	inReplace := false

	searchMatchIndex := -1
	searchEndIndex := -1

	lines := strings.Split(diffContent, "\n")

	// If the very last line looks like a partial marker, drop it — when streaming,
	// the diff may cut off mid-marker and we must not treat "----" as content.
	if n := len(lines); n > 0 && looksLikePartialMarker(lines[n-1]) {
		lines = lines[:n-1]
	}

	for _, line := range lines {
		switch {
		case matchSearchStart(line):
			inSearch = true
			inReplace = false
			currentSearch.Reset()
			continue

		case matchSearchEnd(line):
			// SEARCH section closed; locate it in the original and emit everything
			// up to the match so the result tracks the original until this edit.
			inSearch = false
			inReplace = true

			search := currentSearch.String()
			if search == "" {
				// Empty SEARCH: create-or-replace-whole-file.
				if len(originalContent) == 0 {
					searchMatchIndex = 0 // new file: insert at the top
					searchEndIndex = 0
				} else {
					searchMatchIndex = 0 // replace the entire existing file
					searchEndIndex = len(originalContent)
				}
			} else {
				start, end, ok := locate(originalContent, search, lastProcessedIndex)
				if !ok {
					return "", fmt.Errorf(
						"the SEARCH block:\n%s\n...does not match anything in the file",
						strings.TrimRight(search, "\n"),
					)
				}
				searchMatchIndex, searchEndIndex = start, end
			}

			// Output the untouched original between the previous edit and this one.
			result.WriteString(originalContent[lastProcessedIndex:searchMatchIndex])
			continue

		case matchReplaceEnd(line):
			// REPLACE section closed; the replacement text was already streamed into
			// result line-by-line below. Advance past the matched span and reset.
			if searchMatchIndex == -1 {
				return "", fmt.Errorf("REPLACE marker seen before a matched SEARCH block")
			}
			lastProcessedIndex = searchEndIndex
			inSearch = false
			inReplace = false
			currentSearch.Reset()
			searchMatchIndex = -1
			searchEndIndex = -1
			continue
		}

		// Ordinary content line. We re-add the "\n" we split on; keeping the trailing
		// newline matters so full-line matches line up against the original exactly
		// (cline's note at diff.ts L427-L428).
		switch {
		case inSearch:
			currentSearch.WriteString(line)
			currentSearch.WriteString("\n")
		case inReplace:
			// Append replacement lines immediately once we know where they go — this
			// is what makes streamed application visible incrementally.
			if searchMatchIndex != -1 {
				result.WriteString(line)
				result.WriteString("\n")
			}
		}
	}

	// On the final chunk, append whatever original content follows the last edit.
	if isFinal {
		if lastProcessedIndex < len(originalContent) {
			result.WriteString(originalContent[lastProcessedIndex:])
		}
		// A SEARCH/REPLACE block left open at finalization means the diff was
		// truncated — surface it rather than silently emitting a partial file.
		if inSearch || inReplace {
			return "", fmt.Errorf("incomplete diff: a SEARCH/REPLACE block was still open at end of input")
		}
	}

	return result.String(), nil
}

// locate finds search within original at or after startIndex, returning the
// [start,end) char span. It tries an exact substring first, then a line-trimmed
// fallback. Returns ok=false if neither matches.
func locate(original, search string, startIndex int) (int, int, bool) {
	// 1. Exact substring match, forward from where the last edit ended.
	if i := strings.Index(original[startIndex:], search); i != -1 {
		start := startIndex + i
		return start, start + len(search), true
	}
	// 2. Line-trimmed fallback: match line-by-line ignoring per-line whitespace.
	if start, end, ok := lineTrimmedFallbackMatch(original, search, startIndex); ok {
		return start, end, true
	}
	return 0, 0, false
}

// lineTrimmedFallbackMatch matches searchContent against a run of lines in
// originalContent starting at/after startIndex, comparing each line with leading
// and trailing whitespace stripped. It returns the exact [start,end) char span of
// the matched original lines (NOT the trimmed text), so the splice is byte-exact.
//
// This is the port of diff.ts lineTrimmedFallbackMatch (L51-L103). It rescues the
// common case where the model reproduces a block with slightly different
// indentation than the file on disk.
func lineTrimmedFallbackMatch(original, search string, startIndex int) (int, int, bool) {
	originalLines := strings.Split(original, "\n")
	searchLines := strings.Split(search, "\n")

	// Drop the trailing empty element from search's final "\n".
	if len(searchLines) > 0 && searchLines[len(searchLines)-1] == "" {
		searchLines = searchLines[:len(searchLines)-1]
	}
	if len(searchLines) == 0 {
		return 0, 0, false
	}

	// Find which original line startIndex falls on, so we never match behind an
	// already-applied edit.
	startLine := 0
	idx := 0
	for idx < startIndex && startLine < len(originalLines) {
		idx += len(originalLines[startLine]) + 1 // +1 for the '\n'
		startLine++
	}

	for i := startLine; i <= len(originalLines)-len(searchLines); i++ {
		matched := true
		for j := 0; j < len(searchLines); j++ {
			if strings.TrimSpace(originalLines[i+j]) != strings.TrimSpace(searchLines[j]) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		// Convert the matched line range back to a char span over the original.
		start := 0
		for k := 0; k < i; k++ {
			start += len(originalLines[k]) + 1
		}
		end := start
		for k := 0; k < len(searchLines); k++ {
			end += len(originalLines[i+k]) + 1
		}
		return start, end, true
	}
	return 0, 0, false
}
