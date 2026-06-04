package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider returns a scripted sequence of responses, one per turn, and
// records every request it received. This lets us drive the loop deterministically
// with no network — exactly how cline's AgentRuntime tests use a fake model.
type fakeProvider struct {
	responses []*CreateMessageResponse // consumed in order, one per CreateMessage call
	calls     []CreateMessageRequest   // every request the loop sent us
}

func (f *fakeProvider) CreateMessage(_ context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	f.calls = append(f.calls, req)
	idx := len(f.calls) - 1
	if idx >= len(f.responses) {
		// Past the script: pretend the model is done so a runaway loop still
		// terminates via end_turn rather than hanging the test.
		return &CreateMessageResponse{StopReason: "end_turn"}, nil
	}
	return f.responses[idx], nil
}

// textResp is a finished assistant turn (no tools).
func textResp(text string) *CreateMessageResponse {
	return &CreateMessageResponse{
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: text}},
	}
}

// toolResp is an assistant turn that asks to run one tool.
func toolResp(id, name string, input map[string]interface{}) *CreateMessageResponse {
	return &CreateMessageResponse{
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: "tool_use", ID: id, Name: name, Input: input},
		},
	}
}

// ----------------------------------------------------------------------------
// (1) The loop runs a tool, then completes.
// ----------------------------------------------------------------------------

func TestLoop_RunsToolThenCompletes(t *testing.T) {
	dir := t.TempDir()
	fp := &fakeProvider{responses: []*CreateMessageResponse{
		toolResp("toolu_1", "write_file", map[string]interface{}{
			"path":    "out.txt",
			"content": "hello from s01",
		}),
		textResp("Done — wrote the file."),
	}}

	task := &Task{Provider: fp, Tools: []Tool{NewWriteFileTool(dir)}, MaxTurns: 10}
	final, err := task.Run(context.Background(), "write out.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "Done — wrote the file." {
		t.Fatalf("final text = %q", final)
	}
	// The tool must actually have executed.
	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("expected file written: %v", err)
	}
	if string(got) != "hello from s01" {
		t.Fatalf("file content = %q", string(got))
	}
	if len(fp.calls) != 2 {
		t.Fatalf("expected 2 provider calls (tool turn + final turn), got %d", len(fp.calls))
	}
}

// ----------------------------------------------------------------------------
// (2) An unknown tool name becomes an error tool_result, never a panic.
// ----------------------------------------------------------------------------

func TestLoop_UnknownToolHandledGracefully(t *testing.T) {
	fp := &fakeProvider{responses: []*CreateMessageResponse{
		toolResp("toolu_x", "no_such_tool", map[string]interface{}{}),
		textResp("ok, recovered"),
	}}
	task := &Task{Provider: fp, Tools: []Tool{NewBashTool()}, MaxTurns: 5}

	final, err := task.Run(context.Background(), "do a thing")
	if err != nil {
		t.Fatalf("loop should not error on unknown tool: %v", err)
	}
	if final != "ok, recovered" {
		t.Fatalf("final = %q", final)
	}
	// The SECOND request must carry the error tool_result for the unknown tool.
	if len(fp.calls) < 2 {
		t.Fatalf("expected a follow-up request after the unknown tool, got %d calls", len(fp.calls))
	}
	last := fp.calls[1]
	tr := findToolResult(last.Messages, "toolu_x")
	if tr == nil {
		t.Fatalf("no tool_result for toolu_x in follow-up request: %+v", last.Messages)
	}
	if !tr.IsError {
		t.Fatalf("unknown-tool result should be marked is_error, got %+v", tr)
	}
	if s, _ := tr.ToolContent.(string); !strings.Contains(s, "unknown tool") {
		t.Fatalf("error content = %v, want it to mention 'unknown tool'", tr.ToolContent)
	}
}

// ----------------------------------------------------------------------------
// (3) MaxTurns halts a runaway script that never completes.
// ----------------------------------------------------------------------------

func TestLoop_MaxTurnsEnforced(t *testing.T) {
	// A provider that ALWAYS asks for a tool — without the cap this never ends.
	never := &alwaysToolProvider{}
	task := &Task{Provider: never, Tools: []Tool{NewBashTool()}, MaxTurns: 3}

	_, err := task.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatalf("expected an error when MaxTurns is exceeded")
	}
	if !strings.Contains(err.Error(), "MaxTurns=3") {
		t.Fatalf("error = %v, want it to mention MaxTurns=3", err)
	}
	if never.calls != 3 {
		t.Fatalf("provider should have been called exactly MaxTurns=3 times, got %d", never.calls)
	}
}

type alwaysToolProvider struct{ calls int }

func (a *alwaysToolProvider) CreateMessage(_ context.Context, _ CreateMessageRequest) (*CreateMessageResponse, error) {
	a.calls++
	return toolResp("id", "bash", map[string]interface{}{"command": "true"}), nil
}

// ----------------------------------------------------------------------------
// (4) tool_result threading: history grows user → assistant(tool_use) →
//     user(tool_result) in order, and the result is linked by tool_use id.
// ----------------------------------------------------------------------------

func TestLoop_ToolResultThreadedIntoHistory(t *testing.T) {
	fp := &fakeProvider{responses: []*CreateMessageResponse{
		toolResp("toolu_42", "bash", map[string]interface{}{"command": "echo hi"}),
		textResp("finished"),
	}}
	task := &Task{Provider: fp, Tools: []Tool{NewBashTool()}, MaxTurns: 5}
	if _, err := task.Run(context.Background(), "say hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Inspect the messages the loop sent on its SECOND request — they are the
	// full running history up to that point.
	hist := fp.calls[1].Messages
	if len(hist) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, user-tool_result), got %d: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content[0].Type != "text" {
		t.Fatalf("msg[0] should be the user prompt, got %+v", hist[0])
	}
	if hist[1].Role != "assistant" || hist[1].Content[0].Type != "tool_use" {
		t.Fatalf("msg[1] should be the assistant tool_use, got %+v", hist[1])
	}
	if hist[2].Role != "user" || hist[2].Content[0].Type != "tool_result" {
		t.Fatalf("msg[2] should be the user tool_result, got %+v", hist[2])
	}
	// The tool_result MUST reference the assistant's tool_use id — that linkage
	// is what lets the model match the output to its request.
	if hist[2].Content[0].ToolUseID != "toolu_42" {
		t.Fatalf("tool_result tool_use_id = %q, want toolu_42", hist[2].Content[0].ToolUseID)
	}
}

// ----------------------------------------------------------------------------
// (5) Anthropic request encoding: our types marshal to the wire shape the
//     Anthropic Messages API expects (tagged-union blocks, omitempty fields).
// ----------------------------------------------------------------------------

func TestAnthropicRequestEncoding(t *testing.T) {
	req := CreateMessageRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
		System:    "be brief",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ID: "toolu_1", Name: "bash", Input: map[string]interface{}{"command": "ls"}},
			}},
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "toolu_1", ToolContent: "a\nb"},
			}},
		},
		Tools: []ToolSchema{{
			Name:        "bash",
			Description: "run a command",
			InputSchema: map[string]interface{}{"type": "object"},
		}},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Round-trip into a generic map to assert the wire field names.
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["max_tokens"].(float64) != 1024 {
		t.Fatalf("max_tokens not snake_cased: %v", m["max_tokens"])
	}
	if m["system"] != "be brief" {
		t.Fatalf("system field missing: %v", m["system"])
	}
	msgs := m["messages"].([]interface{})
	// Assistant tool_use block: must have id/name/input, must NOT have text/tool_use_id.
	asst := msgs[1].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if asst["type"] != "tool_use" || asst["id"] != "toolu_1" || asst["name"] != "bash" {
		t.Fatalf("tool_use block wrong: %+v", asst)
	}
	if _, present := asst["text"]; present {
		t.Fatalf("omitempty failed: empty text leaked into tool_use block: %+v", asst)
	}
	if _, present := asst["tool_use_id"]; present {
		t.Fatalf("omitempty failed: tool_use_id leaked into tool_use block: %+v", asst)
	}
	// user tool_result block: content goes under "content", linked via tool_use_id.
	ur := msgs[2].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if ur["type"] != "tool_result" || ur["tool_use_id"] != "toolu_1" || ur["content"] != "a\nb" {
		t.Fatalf("tool_result block wrong: %+v", ur)
	}
}

// findToolResult returns the tool_result block matching id, scanning all messages.
func findToolResult(msgs []Message, id string) *ContentBlock {
	for _, m := range msgs {
		for i := range m.Content {
			b := m.Content[i]
			if b.Type == "tool_result" && b.ToolUseID == id {
				return &b
			}
		}
	}
	return nil
}
