package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// The tool handler interface + the registry.
//
// Upstream this is two pieces:
//   - IToolHandler           (ToolExecutorCoordinator.ts L34-38): name +
//     execute + getDescription.
//   - ToolExecutorCoordinator (same file): a Map<string, IToolHandler> with
//     register / has / getHandler / execute.
//
// We collapse them into ToolHandler + Registry. A loop with one hard-coded tool
// does not scale; cline dispatches ~27 tools by NAME through this coordinator.
// ---------------------------------------------------------------------------

// ToolHandler is one executable capability. Schema() advertises it to the model;
// RequiredParams() lists params the executor must see before it calls Execute
// (mirrors how cline's handlers call ToolValidator.assertRequiredParams first);
// Execute() runs the tool and returns its textual output.
type ToolHandler interface {
	Schema() ToolSchema
	RequiredParams() []string
	Execute(ctx context.Context, params map[string]string) (string, error)
}

// Tool is a back-compat alias for the handler interface name used in prose.
type Tool = ToolHandler

// Registry maps tool name -> handler. It is the Go analogue of
// ToolExecutorCoordinator's `handlers = new Map<string, IToolHandler>()`.
type Registry struct {
	handlers map[string]ToolHandler
}

// NewRegistry returns an empty registry. cline's coordinator is populated by
// registerToolHandlers() (ToolExecutor.ts L201), which loops over the known
// tool names and registers each — we register explicitly in main.go / tests.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]ToolHandler)}
}

// Register adds a handler under its schema name (ToolExecutorCoordinator.register
// L114). Later registrations override earlier ones for the same name.
func (r *Registry) Register(h ToolHandler) {
	r.handlers[h.Schema().Name] = h
}

// Has reports whether a tool name is registered (coordinator.has L128). The
// executor calls this FIRST so an unknown tool becomes an error result rather
// than a dispatch into a nil handler.
func (r *Registry) Has(name string) bool {
	_, ok := r.handlers[name]
	return ok
}

// Get looks up a handler by name (coordinator.getHandler L135).
func (r *Registry) Get(name string) (ToolHandler, bool) {
	h, ok := r.handlers[name]
	return h, ok
}

// Schemas returns every registered tool's schema, sorted by name, for advertising
// to the model (cline builds this list from coordinator handlers). Sorted output
// keeps the demo and snapshots deterministic.
func (r *Registry) Schemas() []ToolSchema {
	out := make([]ToolSchema, 0, len(r.handlers))
	for _, h := range r.handlers {
		out = append(out, h.Schema())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------------------------------------------------------------------------
// Sandbox: every file/exec tool is rooted at a base dir so a demo or a test can
// never read or write outside its scratch space. cline enforces a richer policy
// (.clineignore + workspace-relative resolution, ReadFileToolHandler L6); the
// sandbox is the teaching-sized stand-in for "tools are scoped to a workspace".
// ---------------------------------------------------------------------------

// resolveInSandbox joins rel onto base and rejects any path that escapes it
// (via "../" or an absolute path). Returns the cleaned absolute path.
func resolveInSandbox(base, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	abs := filepath.Join(base, rel)
	cleanBase := filepath.Clean(base)
	if abs != cleanBase && !strings.HasPrefix(abs, cleanBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the sandbox", rel)
	}
	return abs, nil
}

// ---------------------------------------------------------------------------
// read_file — mirrors ReadFileToolHandler. Param: path.
// ---------------------------------------------------------------------------

type ReadFileTool struct{ Base string }

func (t *ReadFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "read_file",
		Description: "Read the contents of a file at the given workspace-relative path.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
			"required":   []string{"path"},
		},
	}
}

func (t *ReadFileTool) RequiredParams() []string { return []string{"path"} }

func (t *ReadFileTool) Execute(_ context.Context, params map[string]string) (string, error) {
	abs, err := resolveInSandbox(t.Base, params["path"])
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	return string(data), nil
}

// ---------------------------------------------------------------------------
// write_to_file — mirrors WriteToFileToolHandler. Params: path, content.
// ---------------------------------------------------------------------------

type WriteToFileTool struct{ Base string }

func (t *WriteToFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "write_to_file",
		Description: "Write content to a file at the given path, creating parent dirs.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string"},
				"content": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *WriteToFileTool) RequiredParams() []string { return []string{"path", "content"} }

func (t *WriteToFileTool) Execute(_ context.Context, params map[string]string) (string, error) {
	abs, err := resolveInSandbox(t.Base, params["path"])
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("write_to_file: %w", err)
	}
	content := params["content"]
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write_to_file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), params["path"]), nil
}

// ---------------------------------------------------------------------------
// list_files — mirrors ListFilesToolHandler. Param: path (defaults to ".").
// ---------------------------------------------------------------------------

type ListFilesTool struct{ Base string }

func (t *ListFilesTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "list_files",
		Description: "List the entries of a directory at the given path.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
			"required":   []string{"path"},
		},
	}
}

func (t *ListFilesTool) RequiredParams() []string { return []string{"path"} }

func (t *ListFilesTool) Execute(_ context.Context, params map[string]string) (string, error) {
	abs, err := resolveInSandbox(t.Base, params["path"])
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("list_files: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

// ---------------------------------------------------------------------------
// execute_command — mirrors ExecuteCommandToolHandler. Param: command.
// Runs in the sandbox dir via the shell. (cline gates this behind approval in
// s04; here it just runs — s03 is registry + dispatch, not the safety model.)
// ---------------------------------------------------------------------------

type ExecuteCommandTool struct{ Base string }

func (t *ExecuteCommandTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "execute_command",
		Description: "Run a shell command in the workspace and return its combined output.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
			"required":   []string{"command"},
		},
	}
}

func (t *ExecuteCommandTool) RequiredParams() []string { return []string{"command"} }

func (t *ExecuteCommandTool) Execute(ctx context.Context, params map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", params["command"])
	cmd.Dir = t.Base
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Non-zero exit is still useful output for the model, so we return the
		// captured text alongside the error rather than discarding it.
		return string(out), fmt.Errorf("execute_command: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
