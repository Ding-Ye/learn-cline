package main

import (
	"flag"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// main.go — drive the ContextManager over a growing conversation.
//
// The demo is fully offline and deterministic: it does NOT call an LLM. It
// builds a synthetic history that overflows a small token budget, then shows the
// before/after message counts and estimated tokens once truncation runs — plus
// proof that the first task message and the recent tail survived.
// ---------------------------------------------------------------------------

// buildConversation fabricates a realistic-looking history: one task message,
// then `turns` rounds of assistant(tool_use) → user(tool_result). Each round
// reads a chunky file so the running token estimate climbs fast.
func buildConversation(turns int) []Message {
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{
			Type: "text",
			Text: "TASK: refactor the auth module and add error handling across the codebase.",
		}}},
	}
	// A blob big enough that a handful of these blow past a small budget.
	blob := strings.Repeat("source-line-of-code; ", 200)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("toolu_%02d", i)
		msgs = append(msgs,
			Message{Role: "assistant", Content: []ContentBlock{
				{Type: "text", Text: fmt.Sprintf("Reading file %d to understand it.", i)},
				{Type: "tool_use", ID: callID, Name: "read_file",
					Input: map[string]interface{}{"path": fmt.Sprintf("src/file%d.go", i)}},
			}},
			Message{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: callID, ToolContent: blob},
			}},
		)
	}
	return msgs
}

// roles renders a compact role timeline, e.g. "u a u a u" — handy for eyeballing
// that the first pair and the recent tail survived truncation.
func roles(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte(m.Role[0]) // 'u' or 'a'
	}
	return b.String()
}

func main() {
	budget := flag.Int("budget", 3000, "token budget (model context window stand-in)")
	keep := flag.Int("keep", 4, "recent messages to preserve in addition to the first pair")
	turns := flag.Int("turns", 12, "number of read-a-file round trips to fabricate")
	flag.Parse()

	cm := NewContextManager(*budget, *keep)
	msgs := buildConversation(*turns)
	req := CreateMessageRequest{Model: "demo", Messages: msgs}

	before := estimateTokens(req)
	fmt.Printf("[s08] budget=%d keepRecent=%d turns=%d\n", cm.Budget, cm.KeepRecent, *turns)
	fmt.Printf("  before: messages=%d  est_tokens=%d  over_budget=%v\n",
		len(msgs), before, cm.overBudget(req))
	fmt.Printf("  roles:  %s\n", roles(msgs))

	start, end, ok := cm.nextTruncationRange(msgs)
	if ok {
		fmt.Printf("  drop range: [%d..%d] (%d messages, even=%v, last-removed role=%q)\n",
			start, end, end-start+1, (end-start+1)%2 == 0, msgs[end].Role)
	} else {
		fmt.Printf("  drop range: none (nothing safe to remove)\n")
	}

	out := cm.truncate(req)
	afterReq := req
	afterReq.Messages = out
	after := estimateTokens(afterReq)

	fmt.Printf("  after:  messages=%d  est_tokens=%d  over_budget=%v\n",
		len(out), after, cm.overBudget(afterReq))
	fmt.Printf("  roles:  %s\n", roles(out))

	// Proof the invariants held.
	truncated := len(out) < len(msgs)
	firstKept := len(out) >= 2 &&
		len(out[0].Content) > 0 && strings.HasPrefix(out[0].Content[0].Text, "TASK:")
	recentKept := len(out) > 0 && sameMessage(out[len(out)-1], msgs[len(msgs)-1])
	// The orphan check only means something AFTER a drop: an unmodified history
	// legitimately has paired tool_results at index 2.
	orphanFree := !truncated || !hasLeadingOrphanToolResult(out)
	fmt.Printf("  truncated=%v\n", truncated)
	fmt.Printf("  invariants: first_task_msg_kept=%v recent_tail_kept=%v no_orphan_tool_result=%v\n",
		firstKept, recentKept, orphanFree)
}

// sameMessage is a cheap identity check used by the demo: same role and same
// first-block text/tool_use id is enough to confirm the recent tail survived.
func sameMessage(a, b Message) bool {
	if a.Role != b.Role || len(a.Content) != len(b.Content) {
		return false
	}
	if len(a.Content) == 0 {
		return true
	}
	return a.Content[0].Text == b.Content[0].Text && a.Content[0].ToolUseID == b.Content[0].ToolUseID
}

// hasLeadingOrphanToolResult checks whether the first message after the kept
// pair is a user message still starting with a tool_result (which truncation
// should have stripped).
func hasLeadingOrphanToolResult(msgs []Message) bool {
	if len(msgs) <= 2 {
		return false
	}
	m := msgs[2]
	if m.Role != "user" {
		return false
	}
	for _, b := range m.Content {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}
