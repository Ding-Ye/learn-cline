package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubApprover is a deterministic human stand-in for tests. It records whether
// it was consulted (so we can assert "the policy auto-approved without asking").
type stubApprover struct {
	decision Decision
	reason   string
	asked    int
}

func (s *stubApprover) Ask(context.Context, string, map[string]interface{}) (Decision, string) {
	s.asked++
	return s.decision, s.reason
}

// newReadPolicy: reads auto-approve everywhere; writes auto-approve only inside
// the given workspace. This is the default cline-style posture used by most
// tests below.
func newReadPolicy(workspace string) *AutoApprovePolicy {
	return &AutoApprovePolicy{
		Workspace: workspace,
		Tools: map[string]ToolPolicy{
			"read_file":  {AutoApprove: true, AutoApproveExternal: true},
			"write_file": {AutoApprove: true, AutoApproveExternal: false},
		},
	}
}

// Test 1: a read-only tool is auto-approved by the policy WITHOUT the human
// being prompted, and Execute actually runs.
func TestReadOnlyAutoApproved(t *testing.T) {
	ws := t.TempDir()
	if err := writeFixture(ws, "notes.txt", "hi"); err != nil {
		t.Fatal(err)
	}
	human := &stubApprover{decision: DecisionReject} // would block if consulted
	gate := NewApprovalGate(newReadPolicy(ws), human)
	read := NewReadFileTool(ws)

	result, decision, ran := gate.Execute(context.Background(), read, "c1",
		map[string]interface{}{"path": "notes.txt"})

	if decision != DecisionAutoApprove {
		t.Fatalf("decision = %v, want auto-approve", decision)
	}
	if human.asked != 0 {
		t.Fatalf("human consulted %d times, want 0 (policy should pre-clear)", human.asked)
	}
	if !ran || !read.Ran {
		t.Fatalf("read_file did not run (ran=%v read.Ran=%v)", ran, read.Ran)
	}
	if result.IsError || result.ToolContent != "hi" {
		t.Fatalf("unexpected tool_result: %+v", result)
	}
}

// Test 2: a write (side-effecting) tool is NOT auto-approved when the policy
// abstains — it escalates to the human. Here the human rejects, so Execute must
// NOT run.
func TestWriteNeedsApprovalAndRejectBlocksExecution(t *testing.T) {
	ws := t.TempDir()
	// Policy with NO write entry → write always escalates to the human.
	policy := &AutoApprovePolicy{
		Workspace: ws,
		Tools:     map[string]ToolPolicy{"read_file": {AutoApprove: true}},
	}
	human := &stubApprover{decision: DecisionReject, reason: "not now"}
	gate := NewApprovalGate(policy, human)
	write := NewWriteFileTool(ws)

	result, decision, ran := gate.Execute(context.Background(), write, "c2",
		map[string]interface{}{"path": "out.txt", "content": "data"})

	if human.asked != 1 {
		t.Fatalf("human consulted %d times, want 1 (write must ask)", human.asked)
	}
	if decision.Allowed() {
		t.Fatalf("decision = %v, want a rejection", decision)
	}
	if ran || write.Ran {
		t.Fatalf("write_file ran despite rejection (ran=%v write.Ran=%v)", ran, write.Ran)
	}
	// The file must not exist — the side effect was blocked.
	if _, err := readFixture(ws, "out.txt"); err == nil {
		t.Fatal("out.txt was created despite rejection")
	}
	if !result.IsError {
		t.Fatalf("rejection tool_result should have IsError=true, got %+v", result)
	}
}

// Test 3: a reject round-trips as a feedback tool_result carrying the call id
// and the human's reason — so the loop can feed it back to the model.
func TestRejectFeedbackBecomesToolResult(t *testing.T) {
	ws := t.TempDir()
	policy := &AutoApprovePolicy{Workspace: ws} // empty allowlist → always ask
	human := &stubApprover{decision: DecisionReject, reason: "please ask first"}
	gate := NewApprovalGate(policy, human)
	write := NewWriteFileTool(ws)

	result, _, ran := gate.Execute(context.Background(), write, "call_xyz",
		map[string]interface{}{"path": "out.txt", "content": "data"})

	if ran {
		t.Fatal("tool ran on rejection")
	}
	if result.Type != "tool_result" {
		t.Fatalf("block type = %q, want tool_result", result.Type)
	}
	if result.ToolUseID != "call_xyz" {
		t.Fatalf("tool_result.tool_use_id = %q, want call_xyz (must echo the call)", result.ToolUseID)
	}
	if !result.IsError {
		t.Fatal("rejection must set IsError=true")
	}
	feedback, _ := result.ToolContent.(string)
	if feedback == "" || !contains(feedback, "please ask first") {
		t.Fatalf("feedback %q should carry the human's reason", feedback)
	}
}

// Test 4: when the human approves, the gated write runs and the file appears.
func TestApproveRunsTheTool(t *testing.T) {
	ws := t.TempDir()
	policy := &AutoApprovePolicy{Workspace: ws} // empty → escalate to human
	human := &stubApprover{decision: DecisionApprove, reason: "ok"}
	gate := NewApprovalGate(policy, human)
	write := NewWriteFileTool(ws)

	result, decision, ran := gate.Execute(context.Background(), write, "c4",
		map[string]interface{}{"path": "out.txt", "content": "approved!"})

	if human.asked != 1 {
		t.Fatalf("human consulted %d times, want 1", human.asked)
	}
	if decision != DecisionApprove {
		t.Fatalf("decision = %v, want approve", decision)
	}
	if !ran || !write.Ran {
		t.Fatalf("write_file did not run after approval (ran=%v)", ran)
	}
	if result.IsError {
		t.Fatalf("approved write should not be an error: %+v", result)
	}
	got, err := readFixture(ws, "out.txt")
	if err != nil || got != "approved!" {
		t.Fatalf("file contents = %q, err=%v; want \"approved!\"", got, err)
	}
}

// Test 5: the auto-approve flag (per-tool, in-workspace) clears a write without
// asking the human — the auto-approve fast path for writes.
func TestAutoApproveFlagApprovesLocalWrite(t *testing.T) {
	ws := t.TempDir()
	human := &stubApprover{decision: DecisionReject} // must NOT be consulted
	gate := NewApprovalGate(newReadPolicy(ws), human)
	write := NewWriteFileTool(ws)

	_, decision, ran := gate.Execute(context.Background(), write, "c5",
		map[string]interface{}{"path": "inside.txt", "content": "local write"})

	if human.asked != 0 {
		t.Fatalf("human consulted %d times, want 0 (auto-approve should clear it)", human.asked)
	}
	if decision != DecisionAutoApprove {
		t.Fatalf("decision = %v, want auto-approve", decision)
	}
	if !ran || !write.Ran {
		t.Fatalf("auto-approved write did not run (ran=%v)", ran)
	}
}

// Test 6: a write whose path escapes the workspace is NOT auto-approved (even
// though the tool is on the allowlist for local writes); it escalates to the
// human. This is the path-outside-workspace safety check.
func TestPathOutsideWorkspaceNotAutoApproved(t *testing.T) {
	ws := t.TempDir()
	// write_file: local auto-approve ON, external OFF (cline's default).
	policy := newReadPolicy(ws)
	human := &stubApprover{decision: DecisionReject, reason: "external write blocked"}
	gate := NewApprovalGate(policy, human)

	// Sanity: the policy itself must not pre-clear the escaping path.
	if ok, _ := policy.shouldAutoApprove("write_file",
		map[string]interface{}{"path": "../escape.txt"}); ok {
		t.Fatal("policy auto-approved a path outside the workspace")
	}

	write := NewWriteFileTool(ws)
	_, decision, ran := gate.Execute(context.Background(), write, "c6",
		map[string]interface{}{"path": "../escape.txt", "content": "x"})

	if human.asked != 1 {
		t.Fatalf("human consulted %d times, want 1 (external write must ask)", human.asked)
	}
	if decision.Allowed() || ran {
		t.Fatalf("external write was allowed (decision=%v ran=%v)", decision, ran)
	}
}

// Test 7: approve-all / yolo pre-clears EVERYTHING, including an external write,
// with no human prompt. The most permissive override.
func TestApproveAllAndYoloApproveEverything(t *testing.T) {
	ws := t.TempDir()
	human := &stubApprover{decision: DecisionReject}

	for _, mode := range []string{"approve-all", "yolo"} {
		policy := &AutoApprovePolicy{Workspace: ws} // empty allowlist on purpose
		if mode == "approve-all" {
			policy.ApproveAll = true
		} else {
			policy.Yolo = true
		}
		gate := NewApprovalGate(policy, human)
		write := NewWriteFileTool(ws)

		_, decision, ran := gate.Execute(context.Background(), write, "c7",
			map[string]interface{}{"path": "../anywhere.txt", "content": "x"})

		if decision != DecisionAutoApprove {
			t.Fatalf("[%s] decision = %v, want auto-approve", mode, decision)
		}
		if !ran {
			t.Fatalf("[%s] write did not run", mode)
		}
	}
	if human.asked != 0 {
		t.Fatalf("human consulted %d times under approve-all/yolo, want 0", human.asked)
	}
}

// Test 8: with no human approver, a non-auto-approved call is auto-rejected
// (headless safe default) rather than panicking or blocking.
func TestNoApproverAutoRejects(t *testing.T) {
	ws := t.TempDir()
	gate := NewApprovalGate(&AutoApprovePolicy{Workspace: ws}, nil)
	write := NewWriteFileTool(ws)

	_, decision, ran := gate.Execute(context.Background(), write, "c8",
		map[string]interface{}{"path": "out.txt", "content": "x"})

	if decision.Allowed() || ran {
		t.Fatalf("headless gate allowed a write (decision=%v ran=%v)", decision, ran)
	}
}

// --- small test helpers ---

func writeFixture(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func readFixture(dir, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	return string(data), err
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
