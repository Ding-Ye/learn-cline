---
title: "s07 · File Edit & Diff Application"
chapter: 7
slug: s07-file-edit-diff
est_read_min: 13
---

# s07 · File Edit & Diff Application

> What this teaches: the **diff applier** behind cline's `replace_in_file` — how a model edits a file by emitting SEARCH/REPLACE blocks, how the applier locates each SEARCH (exact, then line-trimmed) and splices the REPLACE, and why the whole thing is written to apply progressively as the diff streams in.

---

## Problem

By s06 the agent can advertise a rich, model-aware system prompt and dispatch tools through a gated registry. One of those tools writes files — but the only file-writing tool we have is `write_to_file`, which rewrites the **entire** file from a content string. That is fine for creating a new file, and it is how cline first edits anything. It is also a terrible way to make a one-line change to a 500-line file: the model has to re-emit all 500 lines perfectly, it burns output tokens, and any drift corrupts the file.

cline's answer is `replace_in_file`: the model emits only the slices it wants to change, as **SEARCH/REPLACE** blocks. The SEARCH text says "find this exact region"; the REPLACE text says "put this here instead". The pain this chapter solves is twofold. First, *matching*: the SEARCH text the model emits rarely matches the file byte-for-byte (indentation drifts, whitespace differs), so a naive `strings.Index` fails constantly — we need graceful fallbacks. Second, *streaming*: the diff arrives token-by-token from the provider (s05), and cline shows the edit applying live, so the applier must produce a correct partial result from a partial diff and the same final result whether fed in one shot or a hundred chunks.

## Solution

Model the edit as a function over text, not a mutation of a file: `constructNewFileContent(diff, original, isFinal) -> newContent`. It walks the diff one line at a time, tracking a tiny state machine (idle → in-SEARCH → in-REPLACE) and a `lastProcessedIndex` cursor into the original.

Three design decisions carry the chapter:

1. **Tiered matching, in a fixed order.** For each SEARCH block we try an **exact** substring match first (forward from `lastProcessedIndex`, so we never match behind an already-applied edit), then fall back to a **line-trimmed** match that compares lines ignoring per-line whitespace but still splices the *byte-exact* original span. cline adds a third block-anchor tier for 3+ line blocks; we summarize it rather than port it.
2. **The result is built, not patched.** Instead of mutating a buffer at offsets, we *append*: emit `original[last:matchStart]`, then stream the REPLACE lines, then advance `last = matchEnd`; on the final chunk, append the trailing `original[last:]`. Because we only ever append, a partial diff yields a correct prefix of the final result — that is what makes streaming free.
3. **Empty SEARCH is create-or-replace.** An empty SEARCH against an empty original *creates* the file (pure insertion); against a non-empty original it replaces the whole file. This is how the same engine serves both `write_to_file`-style creation and surgical editing.

## How It Works

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────────┐
│  diff lines            original = "a\nb\nc\n"        result        │
│  ------- SEARCH                                       ""            │
│   b              ─┐                                                 │
│  =======         │  locate(b) → [2,4)  exact|line-trimmed          │
│                  │  result += orig[last:2] = "a\n"   → "a\n"       │
│   B              │  stream REPLACE line               → "a\nB\n"   │
│  +++++++ REPLACE ─┘  last = 4                                       │
│                                                                    │
│  (repeat per block, in file order)                                 │
│  isFinal → result += orig[last:] = "c\n"            → "a\nB\nc\n"  │
└──────────────────────────────────────────────────────────────────┘
```

The core 40 lines (excerpt from [`agents/s07-file-edit-diff/diff.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s07-file-edit-diff/diff.go)) — the marker loop and the splice:

```go
for _, line := range lines {
	switch {
	case matchSearchStart(line):
		inSearch = true
		inReplace = false
		currentSearch.Reset()
		continue

	case matchSearchEnd(line):
		// SEARCH closed; locate it in the original and emit everything up to the
		// match so the result tracks the original until this edit.
		inSearch = false
		inReplace = true

		search := currentSearch.String()
		if search == "" {
			if len(originalContent) == 0 { // new file: insert at the top
				searchMatchIndex, searchEndIndex = 0, 0
			} else { // replace the entire existing file
				searchMatchIndex, searchEndIndex = 0, len(originalContent)
			}
		} else {
			start, end, ok := locate(originalContent, search, lastProcessedIndex)
			if !ok {
				return "", fmt.Errorf("the SEARCH block:\n%s\n...does not match anything in the file",
					strings.TrimRight(search, "\n"))
			}
			searchMatchIndex, searchEndIndex = start, end
		}
		result.WriteString(originalContent[lastProcessedIndex:searchMatchIndex])
		continue

	case matchReplaceEnd(line):
		lastProcessedIndex = searchEndIndex // advance past the matched span
		inSearch, inReplace = false, false
		currentSearch.Reset()
		searchMatchIndex, searchEndIndex = -1, -1
		continue
	}

	switch { // ordinary content line — keep the trailing "\n" for exact matching
	case inSearch:
		currentSearch.WriteString(line + "\n")
	case inReplace:
		if searchMatchIndex != -1 { // stream the replacement once we know where it goes
			result.WriteString(line + "\n")
		}
	}
}
```

**Four non-obvious points**:

1. **Why append-only makes streaming free** — because the result is only ever extended (`orig[last:match]`, then replacement lines, then `orig[last:]` at the end), feeding a *prefix* of the diff produces a *prefix* of the final result. There is no separate "streaming code path"; `isFinal` only controls whether we append the trailing original.
2. **Forward-only matching is a feature, not a limitation** — `locate` searches from `lastProcessedIndex`, so a later block can't accidentally match text that an earlier block already consumed. The price is that blocks must appear in file order; cline accepts the same constraint (and bolts on an out-of-order rescue we skip).
3. **Line-trimmed match splices the original, not the trimmed text** — the fallback compares `strings.TrimSpace` of each line to tolerate indentation drift, but the returned `[start,end)` span covers the *real* original characters, so the file's own whitespace is preserved byte-for-byte.
4. **The trailing newline is deliberately kept** — we re-add the `"\n"` we split on. cline's own comment (diff.ts L427) explains why: stripping it would break full-line matching against the original, and we can't tell whether a model's final line is "partial" or just newline-terminated.

## What Changed (vs. s06)

s06 produced a system prompt that *advertises* `write_to_file` (and would advertise `replace_in_file`). s07 makes `replace_in_file` real. The file-tool surface changes from whole-file writes to a diff engine plus a thin tool over it:

```diff
-// s06: the only file-editing tool overwrites the whole file.
-type WriteToFileTool struct{}
-func (WriteToFileTool) Apply(root string, in map[string]any) (ContentBlock, error) {
-    return write(root, in["path"], in["content"])     // blunt: re-emit everything
-}
+// s07: a streaming diff applier underlies a surgical edit tool.
+func constructNewFileContent(diff, original string, isFinal bool) (string, error) { ... }
+
+type ReplaceInFileTool struct{}
+func (ReplaceInFileTool) Apply(root string, in map[string]any) (ContentBlock, error) {
+    updated, err := applyDiffToFile(root, in["path"], in["diff"]) // locate + splice
+    if err != nil { return errorResult(err.Error()), nil }       // feed error back to the model
+    return ContentBlock{Type: "tool_result", ToolContent: "edited " + ...}, nil
+}
```

Semantically, editing stops being "the model regenerates the file" and becomes "the model describes a delta and we apply it". A failed match no longer crashes the loop — it returns an *error* `tool_result`, exactly as cline does, so the model can read "your SEARCH didn't match" and try again.

## Try It

```bash
cd agents/s07-file-edit-diff

# Apply a two-block diff to a seed file and watch it stream (offline, no key).
go run .

# Tests — fully offline, use t.TempDir.
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape (the demo is deterministic — no LLM — so it reproduces byte-for-byte; full transcript in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s07-file-edit-diff/testdata/expected.txt)):

```
== edited main.go ==
package main

import "fmt"

func greeting() string { return "hello, cline" }

func main() {
	fmt.Println(greeting())
}

== streamed application (incremental result length) ==
  after  1/12 diff lines: result is 0 bytes
  after  3/12 diff lines: result is 28 bytes
  ...
  final result == one-shot result: true
```

If the streamed and one-shot results disagree, or a block matches the wrong span, the append-only invariant has been broken — that is the first thing to check.

## Upstream Source Reading

cline's applier lives in `apps/vscode/src/core/assistant-message/diff.ts`. The dispatcher `constructNewFileContent` (L245) picks a version; `constructNewFileContentV1` (L266) is the streaming loop our Go port mirrors. The excerpt below is the heart of it: the per-line marker loop and the tiered match + splice.

```upstream:apps/vscode/src/core/assistant-message/diff.ts#L305-L393
for (const line of lines) {
	if (isSearchBlockStart(line)) {
		inSearch = true
		currentSearchContent = ""
		currentReplaceContent = ""
		continue
	}

	if (isSearchBlockEnd(line)) {
		inSearch = false
		inReplace = true

		if (!currentSearchContent) {
			// Empty search block
			if (originalContent.length === 0) {
				// New file scenario: nothing to match, just start inserting
				searchMatchIndex = 0
				searchEndIndex = 0
			} else {
				// V1 errors here; V2 (and our Go port) treat empty-SEARCH as whole-file replace.
				throw new Error("Empty SEARCH block detected with non-empty file. ...")
			}
		} else {
			// 1) Exact match, forward from the last applied edit.
			const exactIndex = originalContent.indexOf(currentSearchContent, lastProcessedIndex)
			if (exactIndex !== -1) {
				searchMatchIndex = exactIndex
				searchEndIndex = exactIndex + currentSearchContent.length
			} else {
				// 2) Line-trimmed fallback (ignore per-line whitespace).
				const lineMatch = lineTrimmedFallbackMatch(originalContent, currentSearchContent, lastProcessedIndex)
				if (lineMatch) {
					;[searchMatchIndex, searchEndIndex] = lineMatch
				} else {
					// 3) Block-anchor fallback for 3+ line blocks (summarized in our port).
					const blockMatch = blockAnchorFallbackMatch(originalContent, currentSearchContent, lastProcessedIndex)
					if (blockMatch) {
						;[searchMatchIndex, searchEndIndex] = blockMatch
					} else {
						throw new Error(`The SEARCH block:\n${currentSearchContent.trimEnd()}\n...does not match anything in the file.`)
					}
				}
			}
		}

		// For in-order replacements, output everything up to the match location
		result += originalContent.slice(lastProcessedIndex, searchMatchIndex)
		continue
	}
	// isReplaceBlockEnd → advance lastProcessedIndex; content lines accumulate
	// into search or stream into result. (continues to L438)
}
```

**Reading notes**:

- **Match order is identical**: exact → line-trimmed → block-anchor. We port the first two faithfully; the block-anchor tier (diff.ts L132-L185) is summarized because it rarely fires once line-trimming is in place, and porting it would double the matcher code for little teaching value.
- **Empty SEARCH differs on purpose**: V1 *throws* on empty-SEARCH-with-nonempty-file (a malformed-marker guard); V2's `beforeReplace` (L662-L672) treats it as a whole-file replace. Our Go port follows the friendlier V2 rule, which is also what `write_to_file`-as-replace wants.
- **Out-of-order replacements**: V1 carries a `pendingOutOfOrderReplacement` path (L370-L385) and re-sorts replacements at the end. We drop it and require file order, keeping the cursor logic linear and the streaming invariant obvious.
- **The trailing-newline subtlety**: cline keeps the `+ "\n"` and explains (L427-L428) that it cannot strip it without breaking full-line fallback matching — we copy both the code and the reasoning.
- **A correct-but-imperfect choice we keep**: on the final chunk we *error* if a block is still open, rather than emitting a best-effort partial file. cline's V2 does the same (`getResult` L591-L593: "File processing incomplete"); failing loudly beats writing a half-applied edit to disk.

**Read further**: start at `diff.ts` → `constructNewFileContent` (L245), drop into `constructNewFileContentV1` (L266) for the loop above, then read `lineTrimmedFallbackMatch` (L51) and the V2 class `NewFileContentConstructor` (L499). That trace is the real-source map for s07 → s08 (the diff feeds the context that s08 must keep under budget).

---

**Next**: s08 evolves the picture from *editing* to *remembering* — once the agent has read files and applied diffs, the conversation outgrows the model's context window, and s08 truncates old turns under a token budget without orphaning tool results.
