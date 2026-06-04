package main

import "context"

// ---------------------------------------------------------------------------
// Canonical types (the shared "Shared types catalog" subset s04 needs).
//
// Every chapter is a self-contained module, so we re-declare the vocabulary
// instead of importing a shared package. These names match the catalog in
// .learn/plan.md verbatim, so a block written in s01 reads the same here.
//
// s04's job is the APPROVAL GATE, so the load-bearing types are Tool (the thing
// being gated) and ContentBlock (how a tool_result — including a *rejection* —
// is fed back to the model). The wire types are carried along so the demo and
// tests speak the same Anthropic block model as the rest of the curriculum.
// ---------------------------------------------------------------------------

// Message is one turn in the conversation. The API takes an alternating
// user / assistant list plus an optional system prompt.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message: a tagged union over Type. The
// JSON encoder relies on `omitempty` to suppress fields that don't apply.
//
//	"text"        — plain assistant prose (and the initial user prompt)
//	"tool_use"    — the model asks to run a tool (id + name + input)
//	"tool_result" — we feed a tool's output back (tool_use_id + content)
//
// In s04 the tool_result block is where a *rejected* call lands: instead of the
// tool's output, ToolContent carries the user's feedback and IsError is true.
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

// ToolSchema is a tool advertised to the model in the request.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// Tool is one executable capability. Schema() advertises it to the model;
// Execute() runs it AFTER approval. The gate in gate.go sits squarely between
// "the model asked for this tool" and this Execute call.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}
