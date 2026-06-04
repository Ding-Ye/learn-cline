package main

// ---------------------------------------------------------------------------
// Canonical types (subset used by s06).
//
// Every chapter in learn-cline copies the slice of the shared type catalog it
// needs — there is no shared package to import (each sNN is a standalone Go
// module). s06 is about the SYSTEM PROMPT, so the load-bearing type here is
// ToolSchema: the same struct s03 used to advertise a tool to the model. In s03
// a tool was a bare JSON schema in the request's `tools` array. In s06 we render
// that schema into prose INSIDE the prompt text — the XML tool-use section — so
// a model that only reads the system string still learns every tool's name,
// description, and parameters.
//
// The Anthropic-wire types (Message / ContentBlock / CreateMessageRequest) are
// carried for vocabulary continuity with s01-s05; s06 only constructs the
// `System` string and the `Tools` list, never makes a network call.
// ---------------------------------------------------------------------------

// ToolSchema is one tool advertised to the model. Name + Description + the
// JSON-Schema-shaped InputSchema are exactly what cline's ClineToolSpec carries
// per tool; the PromptBuilder turns each of these into a `## <name>` block.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// Message is one turn in the conversation (Anthropic Messages wire shape). Kept
// for continuity; the system prompt is sent alongside this list, not inside it.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a tagged union over Type ("text" | "tool_use" | "tool_result").
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

// CreateMessageRequest is the request the agent loop sends. s06 produces the
// System field (the assembled prompt) and the Tools field; when a variant uses
// native tool calling the Tools array is populated, when it uses XML tools the
// tools live inside System instead and Tools is left empty.
type CreateMessageRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
}
