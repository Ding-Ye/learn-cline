package main

import (
	"encoding/json"
)

// ---------------------------------------------------------------------------
// context.go — context-window management.
//
// A conversation grows every turn (user → assistant → user(tool_result) → ...).
// Models have a fixed context window (~200K tokens for Claude 4, far less for
// small models). When the running history nears that window, cline TRUNCATES:
// it drops a range of OLD middle messages while always keeping the first
// user/assistant pair (the task definition) and the most recent turns (where
// the model is actually working).
//
// This file ports the truncation CORE from cline's ContextManager:
//   - estimateTokens : a char/4 heuristic stand-in for a real tokenizer
//   - nextTruncationRange : which inclusive [start,end] range to drop
//   - truncate : produce the trimmed list, stripping orphaned tool_results
// The trigger is simple: estimate(request) > budget.
//
// Upstream: apps/vscode/src/core/context/context-management/ContextManager.ts
//   getNewContextMessagesAndMetadata (L227), getNextTruncationRange (L299),
//   getAndAlterTruncatedMessages (L344) / applyContextHistoryUpdates (L482).
// ---------------------------------------------------------------------------

// charsPerToken is the rough bytes-per-token ratio used as a stand-in for a real
// tokenizer (cline estimates similarly when an exact count isn't available).
// Tests assert RELATIVE behavior (truncate or not), never an exact token count,
// precisely because this number is approximate.
const charsPerToken = 4

// estimateTokens approximates the token cost of a request by serializing the
// parts that count against the context window — system prompt, messages, and
// tool schemas — and dividing the byte length by charsPerToken.
//
// Why serialize the whole thing? Because the model is billed for the JSON it
// receives: tool schemas, every content block, the role tags. A naive
// len(text)/4 over just the prose would badly under-count a tool-heavy convo.
func estimateTokens(req CreateMessageRequest) int {
	// Marshal can only fail on unencodable values (chan/func); our types are
	// plain data, so in practice this never errors. Fall back to 0 defensively.
	blob, err := json.Marshal(struct {
		System   string       `json:"system"`
		Messages []Message    `json:"messages"`
		Tools    []ToolSchema `json:"tools"`
	}{req.System, req.Messages, req.Tools})
	if err != nil {
		return 0
	}
	return len(blob) / charsPerToken
}

// ContextManager decides when and how to trim a conversation. It is configured
// with one number — the token Budget (the model's usable context window). The
// real ContextManager also persists per-message "context history updates" to
// disk for checkpointing; we keep only the in-memory truncation logic.
type ContextManager struct {
	// Budget is the maximum estimated tokens a request may use before we trim.
	// Mirrors cline's maxAllowedSize (a fraction of the raw context window,
	// leaving headroom for the model's reply).
	Budget int

	// KeepRecent is how many of the most-recent messages to preserve in addition
	// to the first user/assistant pair. cline keeps the tail implicitly via its
	// "half"/"quarter" math; we make it an explicit knob so the intent is clear:
	// the newest turns are where the model is mid-task and must not be dropped.
	KeepRecent int
}

// NewContextManager builds a manager with a token budget and a recent-message
// floor. KeepRecent is rounded UP to an even number so the kept tail starts on
// a user message and ends on the latest assistant message, preserving the
// user-assistant-user-assistant pairing (the same invariant cline guards).
func NewContextManager(budget, keepRecent int) *ContextManager {
	if keepRecent < 0 {
		keepRecent = 0
	}
	if keepRecent%2 != 0 {
		keepRecent++ // even count → kept tail aligns to a user/assistant boundary
	}
	return &ContextManager{Budget: budget, KeepRecent: keepRecent}
}

// overBudget reports whether a request's estimated size exceeds the budget.
// This is the trigger: cline checks the PREVIOUS request's real usage against
// maxAllowedSize; with no live usage we estimate the request we're about to send.
func (cm *ContextManager) overBudget(req CreateMessageRequest) bool {
	return estimateTokens(req) > cm.Budget
}

// nextTruncationRange computes the inclusive [start, end] index range to REMOVE
// from messages. It is a faithful port of cline's getNextTruncationRange
// (ContextManager.ts L299-339), specialized to a single keep-recent strategy.
//
// Invariants (all from upstream):
//   - rangeStart is always 2: indices 0 and 1 (the first user/assistant pair,
//     i.e. the task definition) are NEVER removed.
//   - the count to remove is first computed as an EVEN number (upstream counts
//     pairs, then doubles), aiming to keep the tail aligned to a user boundary.
//   - the last removed message must then be an assistant message; if it lands on
//     a user message we back off by one (upstream's rangeEndIndex -= 1 guard).
//     That final guard can flip the removed count to odd — the load-bearing
//     invariant is the boundary ROLE (kept tail resumes on a user message), not
//     the parity of how many were removed.
//
// Returns ok=false when there is nothing safe to remove (too few messages, or
// removing would eat into the recent tail).
func (cm *ContextManager) nextTruncationRange(messages []Message) (start, end int, ok bool) {
	const rangeStart = 2 // index 0 and 1 are the first user/assistant pair — kept

	// Need at least the first pair + the recent tail + something in between to
	// have anything droppable.
	n := len(messages)
	if n <= rangeStart+cm.KeepRecent {
		return 0, 0, false
	}

	// startOfRest is the first index eligible for deletion (just past the kept
	// first pair). The window we may drop from is [startOfRest, lastKeepable).
	startOfRest := rangeStart
	lastKeepable := n - cm.KeepRecent // first index of the recent tail to PRESERVE

	// How many messages sit between the kept pair and the recent tail.
	droppable := lastKeepable - startOfRest
	if droppable <= 0 {
		return 0, 0, false
	}

	// Remove an EVEN number (upstream divides by 2 to count pairs, floors, then
	// multiplies by 2). Here we drop the whole droppable middle, snapped down to
	// even so the tail stays aligned to a user/assistant boundary.
	messagesToRemove := droppable
	if messagesToRemove%2 != 0 {
		messagesToRemove--
	}
	if messagesToRemove <= 0 {
		return 0, 0, false
	}

	end = startOfRest + messagesToRemove - 1 // inclusive end

	// Ensure the LAST removed message is an assistant message, so the next kept
	// message is a user message. This is upstream's structure-preserving guard
	// (ContextManager.ts L333-335).
	if end >= 0 && end < n && messages[end].Role != "assistant" {
		end--
	}
	if end < startOfRest {
		return 0, 0, false
	}

	return startOfRest, end, true
}

// truncate returns a NEW trimmed message list. If the request is within budget
// (or there is nothing safe to drop) it returns the messages unchanged.
//
// When it does trim, it mirrors cline's getAndAlterTruncatedMessages /
// applyContextHistoryUpdates (ContextManager.ts L344-547): keep messages[0:2],
// drop the computed range, splice the rest back on, then STRIP orphaned
// tool_result blocks from the new first post-truncation message — otherwise the
// model receives a tool_result whose matching tool_use was just deleted, which
// the API rejects.
func (cm *ContextManager) truncate(req CreateMessageRequest) []Message {
	messages := req.Messages
	if len(messages) <= 1 {
		return messages
	}
	if !cm.overBudget(req) {
		return messages
	}

	start, end, ok := cm.nextTruncationRange(messages)
	if !ok {
		return messages // can't safely remove anything; send as-is
	}

	return applyTruncation(messages, start, end)
}

// applyTruncation removes the inclusive index range [start, end] from messages,
// always preserving messages[0:2] (the first user/assistant pair), and removes
// orphaned leading tool_results from the first surviving post-pair message.
//
// Split out from truncate() so tests can drive the splice + orphan-strip logic
// directly with a known range.
func applyTruncation(messages []Message, start, end int) []Message {
	if start < 2 {
		start = 2 // never touch the first pair
	}
	if end >= len(messages) {
		end = len(messages) - 1
	}
	if end < start {
		return messages
	}

	// firstChunk = the kept task definition; tail = everything after the removed
	// range. We allocate a fresh slice so we never mutate the caller's history.
	out := make([]Message, 0, 2+(len(messages)-(end+1)))
	out = append(out, messages[:2]...)
	out = append(out, messages[end+1:]...)

	// Remove orphaned tool_results: the first message after the kept pair may be
	// a user message whose tool_result blocks reference a now-deleted tool_use.
	// Upstream filters those out (ContextManager.ts L492-505) to keep the
	// tool_use/tool_result pairing valid.
	if len(out) > 2 {
		if stripped, changed := stripLeadingToolResults(out[2]); changed {
			out[2] = stripped
		}
	}
	return out
}

// stripLeadingToolResults returns a copy of msg with any tool_result blocks
// removed, if msg is a user message that contains them. It reports whether it
// changed anything so the caller only replaces the slot when needed (and never
// deep-copies unnecessarily).
func stripLeadingToolResults(msg Message) (Message, bool) {
	if msg.Role != "user" {
		return msg, false
	}
	hasToolResult := false
	for _, b := range msg.Content {
		if b.Type == "tool_result" {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		return msg, false
	}

	kept := make([]ContentBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type == "tool_result" {
			continue // orphaned: its tool_use was truncated away
		}
		kept = append(kept, b)
	}
	cp := msg          // shallow copy of the struct
	cp.Content = kept  // with a fresh, filtered Content slice
	return cp, true
}
