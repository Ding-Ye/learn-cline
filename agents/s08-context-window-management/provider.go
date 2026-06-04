package main

// ---------------------------------------------------------------------------
// Wire shape (Anthropic Messages API) — the canonical block model.
//
// Every chapter of learn-cline copies the subset of these types it needs (no
// shared package; each module is self-contained). cline normalizes ~40 providers
// down to this ONE vocabulary (apps/vscode/src/core/api), and the whole agent —
// loop, parser, tool executor, AND the context manager in this chapter — speaks
// only this model.
//
// s08 manipulates a []Message list: it drops a middle range when the running
// token estimate nears the model's context window. So the types it cares about
// most are Message and ContentBlock — in particular the "tool_use" /
// "tool_result" pairing, which truncation must never corrupt.
// ---------------------------------------------------------------------------

// Message is one turn in the conversation. The API takes an alternating
// user / assistant list plus an optional system prompt. cline uses Anthropic
// format throughout, so the list is always user-assistant-user-assistant...
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type; the JSON encoder relies on `omitempty` to suppress fields that
// don't apply to a given block type.
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

// ToolSchema is a tool advertised to the model in the request. s08 estimates
// its serialized size as part of the token budget (tools count against the
// context window just like messages do).
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// CreateMessageRequest is what we send to the model each turn. s08's job is to
// keep estimateTokens(System + Messages + Tools) under the model's budget by
// trimming Messages before this request goes out.
type CreateMessageRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
}

// CreateMessageResponse is the buffered (non-streaming) reply. Its Usage is the
// real signal cline truncates on: when the PREVIOUS request's total token count
// neared the window, trim before the next one. (We use an estimator instead of
// a real tokenizer; see context.go.)
type CreateMessageResponse struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"` // "end_turn" | "tool_use" | ...
	Usage      Usage          `json:"usage"`
}
