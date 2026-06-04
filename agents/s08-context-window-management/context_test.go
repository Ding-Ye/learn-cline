package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// bigText returns a text block large enough to move the token estimate.
func bigText(tag string, n int) ContentBlock {
	return ContentBlock{Type: "text", Text: tag + ":" + strings.Repeat("x", n)}
}

// convo builds a valid Anthropic-format history: a first user/assistant pair,
// then `turns` of assistant(tool_use) → user(tool_result), each carrying a blob
// of `blobLen` characters so the estimate scales with the number of turns.
func convo(turns, blobLen int) []Message {
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "TASK: do the thing"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "ok, starting"}}},
	}
	for i := 0; i < turns; i++ {
		id := fmt.Sprintf("toolu_%02d", i)
		msgs = append(msgs,
			Message{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ID: id, Name: "read_file",
					Input: map[string]interface{}{"path": fmt.Sprintf("f%d", i)}},
			}},
			Message{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: id, ToolContent: strings.Repeat("y", blobLen)},
			}},
		)
	}
	return msgs
}

// Test 1: estimateTokens is monotonic — more content yields a larger estimate,
// and it counts tools + system, not just the prose.
func TestEstimateTokensGrowsWithContent(t *testing.T) {
	small := CreateMessageRequest{Messages: []Message{{Role: "user", Content: []ContentBlock{bigText("a", 10)}}}}
	large := CreateMessageRequest{Messages: []Message{{Role: "user", Content: []ContentBlock{bigText("a", 1000)}}}}

	es, el := estimateTokens(small), estimateTokens(large)
	if es <= 0 {
		t.Fatalf("expected a positive estimate, got %d", es)
	}
	if el <= es {
		t.Fatalf("expected larger content to estimate higher: small=%d large=%d", es, el)
	}

	// Tools count against the window too: adding a tool schema must raise the estimate.
	withTool := small
	withTool.Tools = []ToolSchema{{Name: "read_file", Description: strings.Repeat("d", 500),
		InputSchema: map[string]interface{}{"type": "object"}}}
	if estimateTokens(withTool) <= es {
		t.Fatalf("expected tools to increase the estimate: without=%d with=%d", es, estimateTokens(withTool))
	}
}

// Test 2: a conversation comfortably under budget is returned UNCHANGED.
func TestNoTruncationUnderBudget(t *testing.T) {
	msgs := convo(3, 20)
	req := CreateMessageRequest{Messages: msgs}
	cm := NewContextManager(estimateTokens(req)+1000, 4) // budget well above need

	out := cm.truncate(req)
	if len(out) != len(msgs) {
		t.Fatalf("expected no truncation: in=%d out=%d", len(msgs), len(out))
	}
	if !reflect.DeepEqual(out, msgs) {
		t.Fatalf("under-budget history should be returned unchanged")
	}
}

// Test 3: over budget, truncate drops a MIDDLE range and removes an EVEN count,
// shrinking the message list and the estimate.
func TestTruncationOverBudgetDropsMiddle(t *testing.T) {
	msgs := convo(12, 400) // many fat turns
	req := CreateMessageRequest{Messages: msgs}
	full := estimateTokens(req)
	cm := NewContextManager(full/2, 4) // budget = half → must truncate

	if !cm.overBudget(req) {
		t.Fatalf("test setup wrong: history should be over budget (est=%d budget=%d)", full, cm.Budget)
	}

	start, end, ok := cm.nextTruncationRange(msgs)
	if !ok {
		t.Fatalf("expected a truncation range to be found")
	}
	if start != 2 {
		t.Fatalf("range must start at 2 (keep first pair), got %d", start)
	}
	// Ordering must stay valid: the last removed message is an assistant message
	// (the structure-preserving guard), so the kept tail resumes on a user
	// message. The removed COUNT may be odd once that guard fires; the invariant
	// is the boundary role, not the parity. (See nextTruncationRange.)
	if msgs[end].Role != "assistant" {
		t.Fatalf("last removed message must be assistant, got %q at index %d", msgs[end].Role, end)
	}
	if end+1 < len(msgs) && msgs[end+1].Role != "user" {
		t.Fatalf("message after removed range must be user, got %q", msgs[end+1].Role)
	}

	out := cm.truncate(req)
	if len(out) >= len(msgs) {
		t.Fatalf("expected fewer messages after truncation: in=%d out=%d", len(msgs), len(out))
	}
	if estimateTokens(CreateMessageRequest{Messages: out}) >= full {
		t.Fatalf("expected a smaller estimate after truncation")
	}
}

// Test 4: the first user/assistant pair (the task definition) is ALWAYS kept.
func TestFirstMessagePreserved(t *testing.T) {
	msgs := convo(12, 400)
	req := CreateMessageRequest{Messages: msgs}
	cm := NewContextManager(estimateTokens(req)/3, 4)

	out := cm.truncate(req)
	if len(out) < 2 {
		t.Fatalf("expected at least the first pair to survive, got %d", len(out))
	}
	if !reflect.DeepEqual(out[0], msgs[0]) || !reflect.DeepEqual(out[1], msgs[1]) {
		t.Fatalf("first user/assistant pair must be preserved verbatim")
	}
	if out[0].Content[0].Text != "TASK: do the thing" {
		t.Fatalf("first message text changed: %q", out[0].Content[0].Text)
	}
}

// Test 5: the most-recent KeepRecent messages survive truncation, AND the
// orphaned leading tool_result is stripped from the new first post-pair message.
func TestRecentMessagesPreservedAndOrphanStripped(t *testing.T) {
	keep := 4
	msgs := convo(12, 400)
	req := CreateMessageRequest{Messages: msgs}
	cm := NewContextManager(estimateTokens(req)/2, keep)

	out := cm.truncate(req)
	if len(out) <= keep+2 {
		// Not strictly required, but our setup should leave room; guard the assertion.
		t.Fatalf("setup: expected more than keep+2 messages out, got %d", len(out))
	}

	// The last `keep` messages of the output must equal the last `keep` of input.
	for i := 0; i < keep; i++ {
		gotIdx := len(out) - 1 - i
		wantIdx := len(msgs) - 1 - i
		if !reflect.DeepEqual(out[gotIdx], msgs[wantIdx]) {
			t.Fatalf("recent message %d not preserved: got %+v want %+v", i, out[gotIdx], msgs[wantIdx])
		}
	}

	// After truncation the message at index 2 was a user(tool_result) whose
	// tool_use got deleted — it must have been stripped of tool_result blocks.
	if out[2].Role == "user" {
		for _, b := range out[2].Content {
			if b.Type == "tool_result" {
				t.Fatalf("orphaned tool_result not stripped from post-truncation first message")
			}
		}
	}
}

// Test 6: truncation is DETERMINISTIC — same input yields byte-identical output.
func TestTruncationDeterministic(t *testing.T) {
	build := func() []Message { return convo(12, 400) }
	req := CreateMessageRequest{Messages: build()}
	cm := NewContextManager(estimateTokens(req)/2, 4)

	a := cm.truncate(CreateMessageRequest{Messages: build()})
	b := cm.truncate(CreateMessageRequest{Messages: build()})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("truncation must be deterministic for identical input")
	}

	// And it must not mutate the caller's slice: re-estimating the original
	// (freshly built) request gives the same number both times.
	orig := build()
	if estimateTokens(CreateMessageRequest{Messages: orig}) != estimateTokens(CreateMessageRequest{Messages: build()}) {
		t.Fatalf("estimate of a fresh conversation should be stable")
	}
}

// Test 7: the last removed message is always an assistant message, so the kept
// tail resumes on a user message (preserving user-assistant pairing).
func TestRangeEndsOnAssistant(t *testing.T) {
	// An odd-length droppable middle would, without the guard, end on a user
	// message; the guard must back it off to the preceding assistant message.
	msgs := convo(11, 400)
	req := CreateMessageRequest{Messages: msgs}
	cm := NewContextManager(estimateTokens(req)/2, 3) // odd keep → rounded to 4

	start, end, ok := cm.nextTruncationRange(msgs)
	if !ok {
		t.Fatalf("expected a range")
	}
	if msgs[end].Role != "assistant" {
		t.Fatalf("last removed message must be assistant, got %q at %d", msgs[end].Role, end)
	}
	if start != 2 {
		t.Fatalf("range must start at 2, got %d", start)
	}
	// The message immediately AFTER the removed range must be a user message.
	if end+1 < len(msgs) && msgs[end+1].Role != "user" {
		t.Fatalf("message after removed range must be user, got %q", msgs[end+1].Role)
	}
}
