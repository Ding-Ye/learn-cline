package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestExecutor wires a registry + executor over a sandbox dir, returning both.
func newTestExecutor(t *testing.T) (*ToolExecutor, string) {
	t.Helper()
	base := t.TempDir()
	reg := NewRegistry()
	reg.Register(&ReadFileTool{Base: base})
	reg.Register(&WriteToFileTool{Base: base})
	reg.Register(&ListFilesTool{Base: base})
	reg.Register(&ExecuteCommandTool{Base: base})
	return NewToolExecutor(reg), base
}

// Test: a parsed tool_use block is dispatched to the right handler and the
// result carries the block's CallID (ToolExecutor.execute → pushToolResult).
func TestExecuteDispatchesAndCarriesCallID(t *testing.T) {
	exec, base := newTestExecutor(t)
	if err := os.WriteFile(filepath.Join(base, "f.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := exec.Execute(context.Background(), ToolUse{
		Name:   "read_file",
		CallID: "call_42",
		Params: map[string]string{"path": "f.txt"},
	})
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Output)
	}
	if res.Output != "contents" {
		t.Fatalf("output = %q, want %q", res.Output, "contents")
	}
	if res.CallID != "call_42" {
		t.Fatalf("result CallID = %q, want call_42", res.CallID)
	}

	// And the result converts into a tool_result ContentBlock pairing the id.
	block := res.Block()
	if block.Type != "tool_result" || block.ToolUseID != "call_42" {
		t.Fatalf("block = %+v, want type=tool_result tool_use_id=call_42", block)
	}
}

// Test: an unknown tool name yields an error tool_result, NOT a panic
// (upstream coordinator throws; we convert to an error result the model sees).
func TestExecuteUnknownToolReturnsErrorResult(t *testing.T) {
	exec, _ := newTestExecutor(t)

	res := exec.Execute(context.Background(), ToolUse{Name: "teleport", CallID: "c1"})
	if !res.IsError {
		t.Fatal("unknown tool should produce IsError=true result")
	}
	if !strings.Contains(res.Output, "unknown tool") {
		t.Fatalf("output = %q, want it to mention unknown tool", res.Output)
	}
	if res.CallID != "c1" {
		t.Fatalf("error result should still carry CallID, got %q", res.CallID)
	}
}

// Test: a missing required param is rejected BEFORE the handler runs
// (ToolValidator.assertRequiredParams). write_to_file needs both path+content;
// omitting content must error without creating any file.
func TestExecuteMissingParamRejectedBeforeExecute(t *testing.T) {
	exec, base := newTestExecutor(t)

	res := exec.Execute(context.Background(), ToolUse{
		Name:   "write_to_file",
		CallID: "c2",
		Params: map[string]string{"path": "should-not-exist.txt"}, // content missing
	})
	if !res.IsError {
		t.Fatal("missing required param should produce IsError=true result")
	}
	if !strings.Contains(res.Output, "content") {
		t.Fatalf("output = %q, want it to name the missing 'content' param", res.Output)
	}
	// The file must NOT have been created — validation happened before Execute.
	if _, err := os.Stat(filepath.Join(base, "should-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatal("validation should have short-circuited before write_to_file ran")
	}
}

// Test: write_to_file dispatched through the executor lands bytes on disk, then
// read_file dispatched through the executor reads them back — a round-trip over
// the registry + dispatcher (not the handlers directly).
func TestExecuteWriteThenReadRoundTrip(t *testing.T) {
	exec, _ := newTestExecutor(t)
	ctx := context.Background()

	w := exec.Execute(ctx, ToolUse{
		Name:   "write_to_file",
		CallID: "w1",
		Params: map[string]string{"path": "round/trip.txt", "content": "round-trip body"},
	})
	if w.IsError {
		t.Fatalf("write failed: %q", w.Output)
	}

	r := exec.Execute(ctx, ToolUse{
		Name:   "read_file",
		CallID: "r1",
		Params: map[string]string{"path": "round/trip.txt"},
	})
	if r.IsError {
		t.Fatalf("read failed: %q", r.Output)
	}
	if r.Output != "round-trip body" {
		t.Fatalf("round-trip content = %q, want %q", r.Output, "round-trip body")
	}
}

// Test: the executor accepts a native tool_use block (Input map) just as well as
// an s02 XML block (Params map) — GetParam/flatten unify the two front-ends.
func TestExecuteNativeInputMap(t *testing.T) {
	exec, base := newTestExecutor(t)
	if err := os.WriteFile(filepath.Join(base, "native.txt"), []byte("native ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := exec.Execute(context.Background(), ToolUse{
		Name:   "read_file",
		CallID: "n1",
		Input:  map[string]interface{}{"path": "native.txt"}, // native, not Params
	})
	if res.IsError {
		t.Fatalf("native input dispatch failed: %q", res.Output)
	}
	if res.Output != "native ok" {
		t.Fatalf("output = %q, want %q", res.Output, "native ok")
	}
}
