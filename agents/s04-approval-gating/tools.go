package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// tools.go — two tools that sit on opposite sides of the safety line.
//
//	read_file   — read-only, the canonical AUTO-APPROVE tool
//	write_file  — side-effecting, the canonical ASK-FIRST tool
//
// They each track whether Execute() actually ran (Ran), which is exactly what
// the gate's safety property hangs on: a rejected write must leave Ran == false.
// cline gates ~27 real tools (s03); s04 ships these two so the gate has one of
// each kind to act on.
// ---------------------------------------------------------------------------

// ReadFileTool reads a file. Read-only ⇒ safe to auto-approve.
type ReadFileTool struct {
	baseDir string
	Ran     bool // set true the moment Execute is entered (proves it ran)
}

func NewReadFileTool(baseDir string) *ReadFileTool { return &ReadFileTool{baseDir: baseDir} }

func (r *ReadFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "read_file",
		Description: "Read and return the contents of a workspace file. Read-only.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
}

func (r *ReadFileTool) Execute(_ context.Context, input map[string]interface{}) (string, error) {
	r.Ran = true
	path, _ := input["path"].(string)
	if path == "" {
		return "", fmt.Errorf("input.path must be a non-empty string")
	}
	data, err := os.ReadFile(filepath.Join(r.baseDir, path))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFileTool writes a file. Side-effecting ⇒ must be approved (or auto-
// approved by policy when the path stays inside the workspace).
type WriteFileTool struct {
	baseDir string
	Ran     bool
}

func NewWriteFileTool(baseDir string) *WriteFileTool { return &WriteFileTool{baseDir: baseDir} }

func (w *WriteFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "write_file",
		Description: "Write content to a workspace file. Side-effecting; requires approval.",
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

func (w *WriteFileTool) Execute(_ context.Context, input map[string]interface{}) (string, error) {
	w.Ran = true
	path, _ := input["path"].(string)
	if path == "" {
		return "", fmt.Errorf("input.path must be a non-empty string")
	}
	content, _ := input["content"].(string)
	target := filepath.Join(w.baseDir, path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}
