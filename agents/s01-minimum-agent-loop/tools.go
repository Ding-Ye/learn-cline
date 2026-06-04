package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Tool is the contract every tool must satisfy. The loop only ever sees this
// interface — built-in tools, MCP tools (s09), and skill commands later all
// plug in the same way. cline's ToolExecutor dispatches ~27 of these by name
// (s03); s01 ships two so the loop has something real to run.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

// ---------------------------------------------------------------------------
// WriteFileTool — the canonical destructive tool. cline's write_to_file is
// blunt (whole-file write); s07 teaches the surgical replace_in_file diff.
//
// SAFETY: every path is resolved UNDER baseDir and rejected if it escapes via
// "..". This is the s01 stand-in for cline's workspace boundary; it also lets
// tests point baseDir at t.TempDir() so no real file is ever touched.
// ---------------------------------------------------------------------------

type WriteFileTool struct {
	baseDir string // all writes are confined to this directory
}

func NewWriteFileTool(baseDir string) *WriteFileTool {
	return &WriteFileTool{baseDir: baseDir}
}

func (w *WriteFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "write_file",
		Description: "Write content to a file (creating parent directories as needed). Path is relative to the workspace root.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Workspace-relative file path to write.",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "The full file content.",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (w *WriteFileTool) Execute(_ context.Context, input map[string]interface{}) (string, error) {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("input.path must be a non-empty string, got %T", input["path"])
	}
	content, ok := input["content"].(string)
	if !ok {
		return "", fmt.Errorf("input.content must be a string, got %T", input["content"])
	}

	base := w.baseDir
	if base == "" {
		base = "."
	}
	// Confine the write under baseDir; reject path traversal.
	clean := filepath.Clean(filepath.Join(base, path))
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil || rel == ".." || hasDotDotPrefix(rel) {
		return "", fmt.Errorf("refusing to write outside workspace: %q", path)
	}

	if err := os.MkdirAll(filepath.Dir(absTarget), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(absTarget, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && (rel[2] == filepath.Separator || rel[2] == '/')
}

// ---------------------------------------------------------------------------
// BashTool — run a shell command. cline's execute_command does the same with a
// terminal integration and streaming; s01 keeps it to a single buffered call.
// ---------------------------------------------------------------------------

type BashTool struct{}

func NewBashTool() *BashTool { return &BashTool{} }

func (b *BashTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "bash",
		Description: "Run a shell command via /bin/bash -c and return combined stdout+stderr.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "The shell command to execute.",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (b *BashTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	cmd, ok := input["command"].(string)
	if !ok {
		return "", fmt.Errorf("input.command must be a string, got %T", input["command"])
	}
	out, err := exec.CommandContext(ctx, "bash", "-c", cmd).CombinedOutput()
	// Surface a non-zero exit as content (so the model SEES stderr) rather than
	// as a Go error: from the model's POV a command that "failed" is still a
	// valid tool result it can reason about and recover from.
	if err != nil {
		return fmt.Sprintf("(exit error: %v)\n%s", err, string(out)), nil
	}
	return string(out), nil
}
