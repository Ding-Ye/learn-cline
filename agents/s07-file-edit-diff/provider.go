package main

// ---------------------------------------------------------------------------
// Canonical types (the learn-cline shared vocabulary, copied — never imported).
//
// Every chapter is a self-contained module, so each one re-declares the slice of
// the shared catalog it actually uses. s07 is about FILE EDITING, so it needs the
// tool vocabulary (a tool advertised to the model + executed after parsing) and
// the content-block model that wraps a tool's result back into the conversation.
//
// These are the same shapes you saw in s03 (tool registry) and s05 (provider):
// the wire format is the Anthropic Messages API, a tagged union over Type.
// ---------------------------------------------------------------------------

// ContentBlock is one item inside an assistant/user message. The wire format is
// a tagged union over Type ("text" | "tool_use" | "tool_result"); `omitempty`
// suppresses fields that don't apply to a given block type. In s07 a
// replace_in_file call arrives as a "tool_use" block and its outcome goes back
// as a "tool_result".
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

// ToolSchema is a tool advertised to the model (cline's tool spec). s07 ships two
// editing tools: write_to_file (whole-file) and replace_in_file (SEARCH/REPLACE).
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// Tool is one executable capability. Schema() advertises it to the model;
// Apply() runs it against a sandbox root. (s03 called this Execute; here the
// surface is narrowed to file editing, so the two tools share a tiny interface.)
type Tool interface {
	Schema() ToolSchema
	Apply(root string, input map[string]interface{}) (ContentBlock, error)
}
