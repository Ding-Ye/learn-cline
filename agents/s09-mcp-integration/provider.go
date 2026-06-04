package main

// ---------------------------------------------------------------------------
// Canonical wire shape (Anthropic Messages API) + the tool vocabulary.
//
// Every learn-cline chapter copies the subset of the shared types catalog it
// needs — no cross-module imports (see .learn/plan.md "Shared types catalog").
// s09 needs ToolSchema (the thing a tool advertises to the model) and the
// tool_use / tool_result block model, because the whole point of the chapter is
// that an MCP server's remote tools become ordinary ToolSchema entries in the
// registry — indistinguishable, to the model, from the compiled-in tools of s03.
// ---------------------------------------------------------------------------

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type; `omitempty` suppresses fields that don't apply to a block type.
// s09 only produces "tool_result" blocks (the output of an MCP tools/call,
// folded back into the conversation), but the full union is kept so the type is
// the same one s03 used.
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

// ToolSchema is a tool advertised to the model in the request. This is the
// canonical currency of the chapter: ListTools converts each remote MCP tool
// definition into one of these, so a server-provided tool slots into the same
// registry as any local tool. cline does the same — McpHub.fetchToolsList
// returns McpTool objects that are rendered into the system prompt's tool list
// next to the built-in tools (apps/vscode/src/services/mcp/McpHub.ts L677).
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// Tool is one executable capability. Schema() advertises it to the model;
// Execute() runs it. A compiled-in tool (s03) implements this directly; an MCP
// tool implements it by routing Execute to the server over JSON-RPC (mcp.go's
// mcpTool). Either way the registry below holds Tools, not knowing which is
// which — that uniformity is what makes "discovered" tools first-class.
type Tool interface {
	Schema() ToolSchema
	Execute(args map[string]interface{}) (string, error)
}

// Registry is the name → tool map the agent advertises and dispatches through
// (a trimmed version of s03's ToolExecutorCoordinator). s09 adds one verb to it,
// Merge, which folds a whole batch of discovered MCP tools in at once.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds (or replaces) one tool by its advertised name.
func (r *Registry) Register(t Tool) {
	r.tools[t.Schema().Name] = t
}

// Merge folds a batch of tools (e.g. everything an MCP server exposed) into the
// registry. This is the moment a remote server's capabilities become part of
// the agent's tool set — the structural change s09 is about.
func (r *Registry) Merge(tools []Tool) {
	for _, t := range tools {
		r.Register(t)
	}
}

// Get looks up a tool by name. ok is false for an unknown name so the caller can
// hand the model an error result instead of panicking (the s03 discipline).
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Schemas returns every registered tool's schema, name-sorted, so the advertised
// tool list is deterministic regardless of registration / discovery order.
func (r *Registry) Schemas() []ToolSchema {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]ToolSchema, 0, len(names))
	for _, name := range names {
		out = append(out, r.tools[name].Schema())
	}
	return out
}

// sortStrings is a tiny insertion sort (stdlib-only spirit; avoids pulling sort
// for one slice and keeps the chapter's surface minimal).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
