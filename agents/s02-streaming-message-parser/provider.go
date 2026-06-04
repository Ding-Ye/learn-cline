package main

// ---------------------------------------------------------------------------
// Canonical types (redeclared per the shared types catalog in .learn/plan.md).
//
// Every learn-cline chapter is a self-contained module — no chapter imports
// another — so each one copies the slice of the catalog it needs. s02 needs the
// streaming + parser vocabulary, not the HTTP provider, so this file is mostly
// the STREAM and PARSER types. The wire-shape Message/ContentBlock are carried
// along for continuity with s01 (and because the parser's output maps onto
// them in s03).
//
// WHY a parser-specific block type at all? s01 used the Anthropic NATIVE
// tool_use block: the API already split tool calls into structured JSON for us.
// cline also supports models that DON'T do that — they emit tool calls as
// XML-ish text inside the normal assistant prose. For those, cline parses the
// tool call out of the text stream itself. That parser is this chapter, and its
// output is AssistantBlock (the catalog's AssistantMessageBlock, trimmed to the
// names this chapter teaches).
// ---------------------------------------------------------------------------

// ---- Wire shape (Anthropic Messages API) — same as s01 ----

// Message is one turn; the API takes an alternating user/assistant list plus an
// optional system prompt.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a tagged union over Type: "text" | "tool_use" | "tool_result".
// omitempty suppresses fields that don't apply to a given block type.
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

// ---- Streaming (cline's ApiStream is an async generator of chunks) ----

// StreamEvent mirrors cline's ApiStreamChunk union
// (apps/vscode/src/core/api/transform/stream.ts). For s02 only the "text" delta
// matters: cline's XML tool calls arrive INSIDE the text stream, so the parser
// is fed the Text of each "text" event as it lands. The other variants are kept
// for vocabulary continuity (s05 fills them in for a real SSE client).
type StreamEvent struct {
	Type string `json:"type"` // "text" | "tool_use_start" | "tool_use_delta" | "usage" | "done"

	Text string `json:"text,omitempty"` // type == "text"

	ToolCallID string `json:"tool_call_id,omitempty"` // tool_use_start/delta
	ToolName   string `json:"tool_name,omitempty"`    // tool_use_start
	InputJSON  string `json:"input_json,omitempty"`   // tool_use_delta (partial)

	Usage *Usage `json:"usage,omitempty"` // type == "usage"
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ---- Parser output union ----

// AssistantBlock is the parser output union (the catalog's
// AssistantMessageBlock; "Type" is "text" | "tool_use").
//
//	Type    — "text" for prose, "tool_use" for a parsed XML tool call.
//	Text    — the prose, for Type == "text".
//	ToolName/Params — the tool name and its <param>…</param> values, for "tool_use".
//	Partial — true when the stream cut off mid-block (open tag, no close yet).
//	          This is the WHOLE POINT of streaming: a partial tool_use lets the
//	          UI render "cline is about to write a file…" before the bytes finish.
type AssistantBlock struct {
	Type     string            `json:"type"` // "text" | "tool_use"
	Text     string            `json:"text,omitempty"`
	ToolName string            `json:"tool_name,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Partial  bool              `json:"partial"`
}
