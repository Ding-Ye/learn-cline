package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// (6) OpenAI translate round-trip: an Anthropic-style request with text,
// assistant tool_use, user tool_result, and a tool schema must survive the
// translation to the OpenAI Chat Completions shape with nothing lost.
func TestOpenAITranslate_RequestRoundTrip(t *testing.T) {
	req := CreateMessageRequest{
		System: "be brief",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "run ls"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "text", Text: "sure"},
				{Type: "tool_use", ID: "call_1", Name: "bash", Input: map[string]interface{}{"command": "ls"}},
			}},
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "call_1", ToolContent: "a\nb"},
			}},
		},
		Tools: []ToolSchema{{
			Name:        "bash",
			Description: "run a command",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
				"required":   []string{"command"},
			},
		}},
	}

	out := translateRequestToOpenAI(req, "deepseek-chat", 2048)

	if out.Model != "deepseek-chat" || out.MaxTokens != 2048 {
		t.Fatalf("model/max_tokens wrong: %+v", out)
	}
	// system, user, assistant(+tool_calls), tool → 4 OpenAI messages.
	if len(out.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "be brief" {
		t.Fatalf("system message wrong: %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "run ls" {
		t.Fatalf("user message wrong: %+v", out.Messages[1])
	}
	// assistant: text content + one tool_call carrying the JSON-encoded args.
	asst := out.Messages[2]
	if asst.Role != "assistant" || asst.Content != "sure" || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant message wrong: %+v", asst)
	}
	tc := asst.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "bash" {
		t.Fatalf("tool_call wrong: %+v", tc)
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil || args["command"] != "ls" {
		t.Fatalf("tool_call args not round-tripped: %q (%v)", tc.Function.Arguments, err)
	}
	// tool_result → a {role:"tool"} message keyed by tool_call_id.
	tool := out.Messages[3]
	if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Content != "a\nb" {
		t.Fatalf("tool message wrong: %+v", tool)
	}
	// tool schema → {type:"function", function:{...}} with parameters preserved.
	if len(out.Tools) != 1 || out.Tools[0].Type != "function" || out.Tools[0].Function.Name != "bash" {
		t.Fatalf("tool def wrong: %+v", out.Tools)
	}
	props, ok := out.Tools[0].Function.Parameters["properties"].(map[string]interface{})
	if !ok || props["command"] == nil {
		t.Fatalf("tool parameters lost: %+v", out.Tools[0].Function.Parameters)
	}
}

// (7) The inverse direction: an OpenAI response with a tool_call becomes an
// Anthropic-style response with a tool_use block and stop_reason "tool_use" —
// the exact shape the loop's switch expects.
func TestOpenAITranslate_ResponseToolCallBecomesToolUse(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{
			Index:        0,
			FinishReason: "tool_calls",
			Message: openAIMessage{
				Role: "assistant",
				ToolCalls: []openAIToolCall{{
					ID:       "call_abc",
					Type:     "function",
					Function: openAIToolCallFunc{Name: "bash", Arguments: `{"command":"date"}`},
				}},
			},
		}},
		Usage: openAIUsage{PromptTokens: 11, CompletionTokens: 7},
	}
	out := translateResponseFromOpenAI(resp)
	if out.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", out.StopReason)
	}
	if len(out.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(out.Content))
	}
	b := out.Content[0]
	if b.Type != "tool_use" || b.ID != "call_abc" || b.Name != "bash" || b.Input["command"] != "date" {
		t.Fatalf("tool_use block wrong: %+v", b)
	}
	if out.Usage.InputTokens != 11 || out.Usage.OutputTokens != 7 {
		t.Fatalf("usage wrong: %+v", out.Usage)
	}
}

// (8) finish_reason "stop" maps to "end_turn"; plain text content survives.
// This is the path that ends the loop, so it's worth pinning down.
func TestOpenAITranslate_ResponsePlainTextStop(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{
			{FinishReason: "stop", Message: openAIMessage{Role: "assistant", Content: "all done"}},
		},
	}
	out := translateResponseFromOpenAI(resp)
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", out.StopReason)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "all done" {
		t.Fatalf("content wrong: %+v", out.Content)
	}
}

// (9) A single Anthropic user message that mixes text AND tool_result must
// split into a user message followed by a tool message — OpenAI can't carry
// both in one. This is the trickiest translation edge.
func TestOpenAITranslate_MixedUserBlocksSplit(t *testing.T) {
	req := CreateMessageRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{
			{Type: "text", Text: "also this"},
			{Type: "tool_result", ToolUseID: "call_1", ToolContent: "ok"},
		}}},
	}
	out := translateRequestToOpenAI(req, "x", 0)
	if len(out.Messages) != 2 {
		t.Fatalf("expected user + tool messages, got %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != "user" || out.Messages[1].Role != "tool" {
		t.Fatalf("ordering wrong: %+v", out.Messages)
	}
	if !strings.Contains(out.Messages[1].ToolCallID, "call_1") {
		t.Fatalf("tool message lost its id: %+v", out.Messages[1])
	}
}
