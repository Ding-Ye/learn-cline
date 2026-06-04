package main

// ---------------------------------------------------------------------------
// Canonical wire shape (Anthropic Messages API).
//
// Every learn-cline chapter copies the subset of the shared types catalog it
// needs — no cross-module imports (see .learn/plan.md "Shared types catalog").
// s03 only needs the block model and a tool_use representation; it does NOT
// make a real network call, so the Provider interface and HTTP client from s01
// are intentionally absent here.
//
// cline talks to ~40 providers but normalizes every one to this Anthropic block
// model internally (apps/vscode/src/core/api). Keeping one block model means the
// executor never has to know which provider produced a tool call.
// ---------------------------------------------------------------------------

// Message is one turn in the conversation. The API takes an alternating
// user / assistant list plus an optional system prompt.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type; `omitempty` suppresses fields that don't apply to a block type.
//
// s03 uses two block types:
//
//	"tool_use"    — the model asks to run a tool (id + name + input)
//	"tool_result" — we feed a tool's output back (tool_use_id + content)
type ContentBlock struct {
	Type string `json:"type"`

	// type == "text"
	Text string `json:"text,omitempty"`

	// type == "tool_use"
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	// type == "tool_result"
	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
	IsError     bool        `json:"is_error,omitempty"`
}

// ToolSchema is a tool advertised to the model in the request. Each registered
// Tool produces one of these via Schema() so the model knows it exists.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ToolUse is one parsed tool call: the executor's input. In s02 these come out
// of the streaming XML parser (params as strings); the native Anthropic path
// (s01) delivers Input as a decoded map. s03 carries BOTH so the executor works
// regardless of which front-end produced the call:
//
//	Input  — native tool_use: a decoded JSON object {"path": "x", ...}
//	Params — s02 XML tool_use: raw string params {"path": "x", ...}
//
// GetParam reads Params first, then Input, so a handler never cares which
// front-end produced the call. CallID echoes back into the tool_result so the
// model can pair request and response.
type ToolUse struct {
	Name   string                 `json:"name"`
	CallID string                 `json:"call_id,omitempty"`
	Input  map[string]interface{} `json:"input,omitempty"`
	Params map[string]string      `json:"params,omitempty"`
}

// ToolResult is what the executor hands back to the loop: the formatted output
// of one tool call, ready to become a "tool_result" ContentBlock. IsError marks
// a failed call so the model sees the error instead of the loop crashing —
// cline does exactly this (ToolExecutor.handleError → pushToolResult).
type ToolResult struct {
	CallID  string `json:"call_id,omitempty"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// Block converts a ToolResult into the "tool_result" ContentBlock that goes
// back into the conversation as a user turn. This is the bridge from "a tool
// ran" to "the model can see what happened".
func (r ToolResult) Block() ContentBlock {
	return ContentBlock{
		Type:        "tool_result",
		ToolUseID:   r.CallID,
		ToolContent: r.Output,
		IsError:     r.IsError,
	}
}
