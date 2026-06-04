package main

// edit.go — the two file-editing tools that sit on top of the diff engine.
//
// In s03 the file tools read or wrote WHOLE files. s07 adds surgical editing:
//   - write_to_file   : whole-file create/overwrite (cline's blunt instrument)
//   - replace_in_file : apply a SEARCH/REPLACE diff to an existing file
//
// Both are sandboxed: every path is resolved under a root dir and rejected if it
// escapes, so a tool call can never touch a file outside the workspace.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeJoin resolves rel under root and refuses paths that escape it (via "..").
// cline relies on the editor host for this; in a headless port we guard directly.
func safeJoin(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	full := filepath.Join(root, clean)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the sandbox root", rel)
	}
	return fullAbs, nil
}

// writeFile creates/overwrites a file (and any parent dirs) with content.
func writeFile(root, rel, content string) error {
	full, err := safeJoin(root, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// applyDiffToFile reads rel, applies the SEARCH/REPLACE diff, and writes it back.
// It is the one-shot (isFinal=true) path; streaming is exercised in tests and the
// demo by calling constructNewFileContent directly with growing chunks.
func applyDiffToFile(root, rel, diff string) (string, error) {
	full, err := safeJoin(root, rel)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	updated, err := constructNewFileContent(diff, string(raw), true)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return updated, nil
}

// ---------------------------------------------------------------------------
// Tool wrappers — so the editor slots into an s03-style registry/executor.
// ---------------------------------------------------------------------------

// WriteToFileTool overwrites or creates a file from a full content string.
type WriteToFileTool struct{}

func (WriteToFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "write_to_file",
		Description: "Create a file or overwrite it entirely with the given content.",
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

func (WriteToFileTool) Apply(root string, input map[string]interface{}) (ContentBlock, error) {
	path, _ := input["path"].(string)
	content, _ := input["content"].(string)
	if path == "" {
		return errorResult("write_to_file requires a 'path'"), nil
	}
	if err := writeFile(root, path, content); err != nil {
		return errorResult(err.Error()), nil
	}
	return ContentBlock{Type: "tool_result", ToolContent: "wrote " + path}, nil
}

// ReplaceInFileTool applies a SEARCH/REPLACE diff to an existing file.
type ReplaceInFileTool struct{}

func (ReplaceInFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "replace_in_file",
		Description: "Apply one or more ------- SEARCH / ======= / +++++++ REPLACE blocks to a file.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
				"diff": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path", "diff"},
		},
	}
}

func (ReplaceInFileTool) Apply(root string, input map[string]interface{}) (ContentBlock, error) {
	path, _ := input["path"].(string)
	diff, _ := input["diff"].(string)
	if path == "" || diff == "" {
		return errorResult("replace_in_file requires 'path' and 'diff'"), nil
	}
	updated, err := applyDiffToFile(root, path, diff)
	if err != nil {
		// A failed match comes back as an error tool_result so the model can retry,
		// rather than crashing the loop (cline feeds the diff error back too).
		return errorResult(err.Error()), nil
	}
	return ContentBlock{Type: "tool_result", ToolContent: "edited " + path + "\n--- new content ---\n" + updated}, nil
}

// errorResult wraps a message as a failed tool_result block.
func errorResult(msg string) ContentBlock {
	return ContentBlock{Type: "tool_result", ToolContent: msg, IsError: true}
}
