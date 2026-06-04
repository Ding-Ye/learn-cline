package main

import (
	"context"
	"fmt"
	"strings"
)

// Task is the agent loop. It is deliberately the smallest thing that deserves
// the name: one provider, a list of tools, a hard turn cap.
//
// This mirrors cline's Task (apps/vscode/src/core/task/index.ts). There the
// loop is split across two methods:
//
//	initiateTaskLoop()            — the `while (!abort)` driver
//	recursivelyMakeClineRequests() — one turn: request → assistant → tools
//
// The KEY INSIGHT they encode (and what this whole chapter is about): an agent
// is not the LLM call, it is the LOOP around it. A model that asks to read a
// file just *stops* — unless something runs the tool, feeds the result back,
// and asks the model again. That "ask again with the result" step is the loop,
// and it is the recursion (cline keeps re-issuing requests "until he either
// attempt_completion or does not use anymore tools").
type Task struct {
	Provider Provider
	Tools    []Tool
	MaxTurns int  // safety cap — cline uses maxConsecutiveMistakes / request limits
	Verbose  bool // print every turn (assistant text + tool calls)
}

// Run drives the conversation from a single user prompt to a final answer.
//
// The cycle, once per turn:
//
//	1. send the whole message history (+ tool schemas) to the provider
//	2. append the assistant turn to history (REQUIRED even when it's a tool_use:
//	   the provider must see its own prior tool_use to match the tool_result)
//	3. branch on stop_reason:
//	     end_turn   → the model is done; return its text
//	     tool_use   → run each requested tool, append the results as the NEXT
//	                  user message, and loop (this is the recursion)
//	     max_tokens → the response was cut off; bail with an error
//	4. if we exceed MaxTurns, bail — never loop forever
func (t *Task) Run(ctx context.Context, userPrompt string) (string, error) {
	// Index tools by name once, and snapshot their schemas for the request.
	toolByName := map[string]Tool{}
	schemas := make([]ToolSchema, 0, len(t.Tools))
	for _, tool := range t.Tools {
		s := tool.Schema()
		toolByName[s.Name] = tool
		schemas = append(schemas, s)
	}

	// The conversation starts with the user's prompt. cline seeds history the
	// same way (the task text becomes the first user message).
	messages := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: userPrompt}}},
	}

	for turn := 0; turn < t.MaxTurns; turn++ {
		resp, err := t.Provider.CreateMessage(ctx, CreateMessageRequest{
			Messages: messages,
			Tools:    schemas,
		})
		if err != nil {
			return "", fmt.Errorf("turn %d: %w", turn, err)
		}

		// 1. Append the assistant turn — even if it contains tool_use blocks,
		// the protocol requires the assistant message to live in history so the
		// next request's tool_result blocks have a matching tool_use to point at.
		messages = append(messages, Message{Role: "assistant", Content: resp.Content})

		if t.Verbose {
			t.dumpAssistant(turn, resp)
		}

		// 2. stop_reason tells us what to do next. This switch is the loop's
		// brain: it is where "keep going" vs "we're done" is decided.
		switch resp.StopReason {
		case "end_turn", "stop_sequence":
			// No tool calls → the model considers the task complete. (cline's
			// recursivelyMakeClineRequests returns didEndLoop here; a real
			// attempt_completion tool, taught later, makes this explicit.)
			return extractText(resp.Content), nil

		case "tool_use":
			// 3. Run every tool the assistant asked for, collect tool_result
			// blocks, and feed them back as a single USER message. Then loop.
			toolResults := t.runTools(ctx, resp.Content, toolByName, turn)
			messages = append(messages, Message{Role: "user", Content: toolResults})

		case "max_tokens":
			return "", fmt.Errorf("hit max_tokens at turn %d (response was truncated)", turn)

		default:
			return "", fmt.Errorf("unexpected stop_reason %q at turn %d", resp.StopReason, turn)
		}
	}

	// 4. Turn cap reached. Without this an adversarial / confused model could
	// loop forever; cline guards the same risk with request + mistake limits.
	return "", fmt.Errorf("loop exceeded MaxTurns=%d without end_turn", t.MaxTurns)
}

// runTools executes each tool_use block in the assistant message and returns
// one tool_result block per call, in order. An unknown tool name or an
// execution error becomes an ERROR tool_result rather than aborting the loop —
// the model gets to see the failure and recover, just like cline feeds tool
// errors back as content.
func (t *Task) runTools(ctx context.Context, content []ContentBlock, byName map[string]Tool, turn int) []ContentBlock {
	var results []ContentBlock
	for _, block := range content {
		if block.Type != "tool_use" {
			continue
		}
		tool, ok := byName[block.Name]
		if !ok {
			results = append(results, ContentBlock{
				Type:        "tool_result",
				ToolUseID:   block.ID,
				ToolContent: fmt.Sprintf("unknown tool: %q", block.Name),
				IsError:     true,
			})
			continue
		}
		if t.Verbose {
			fmt.Printf("[turn %d] -> %s %v\n", turn, block.Name, block.Input)
		}
		out, err := tool.Execute(ctx, block.Input)
		isErr := false
		if err != nil {
			out = fmt.Sprintf("tool error: %v", err)
			isErr = true
		}
		if t.Verbose {
			fmt.Printf("[turn %d] <- %s\n", turn, truncate(out, 240))
		}
		results = append(results, ContentBlock{
			Type:        "tool_result",
			ToolUseID:   block.ID, // links the result back to the assistant's tool_use
			ToolContent: out,
			IsError:     isErr,
		})
	}
	return results
}

func (t *Task) dumpAssistant(turn int, resp *CreateMessageResponse) {
	for _, b := range resp.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			fmt.Printf("[turn %d] assistant: %s\n", turn, b.Text)
		}
	}
}

// extractText concatenates the text blocks of a final assistant message.
func extractText(content []ContentBlock) string {
	var sb strings.Builder
	for _, b := range content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
