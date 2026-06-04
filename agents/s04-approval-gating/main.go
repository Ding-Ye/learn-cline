package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// main.go — drive the approval gate over a scripted batch of tool calls.
//
// The demo is offline and deterministic: it does NOT call an LLM. It plays a
// fixed list of tool_use blocks (the kind s02/s03 would parse out of a stream)
// through the ApprovalGate so you can watch the policy auto-approve the safe
// ones and the human get asked about the risky ones.
//
// Two human approvers ship:
//   - StdinApprover    : real Y/N prompt (cline's askApproval transport)
//   - AlwaysApprover/-y : the injected auto-approver the task asks for, used by
//                         the default run so `go run .` needs no interaction.
// ---------------------------------------------------------------------------

// StdinApprover asks the user Y/N on the terminal. This is the s04 stand-in for
// cline's askApproval (UIHelpers.ts L56): ask the human, treat "yes" as approve.
type StdinApprover struct{ in *bufio.Reader }

func NewStdinApprover() *StdinApprover { return &StdinApprover{in: bufio.NewReader(os.Stdin)} }

func (s *StdinApprover) Ask(_ context.Context, toolName string, params map[string]interface{}) (Decision, string) {
	fmt.Fprintf(os.Stderr, "  ? approve %q %v ? [y/N]: ", toolName, params)
	line, _ := s.in.ReadString('\n')
	if strings.EqualFold(strings.TrimSpace(line), "y") {
		return DecisionApprove, "user approved"
	}
	return DecisionReject, "user rejected"
}

// FixedApprover is an injected human stand-in that returns the same Decision
// for every escalation, with no prompt. The demo defaults to one that REJECTS
// (so the escaping write shows the rejection-feedback path); `-y` swaps in one
// that approves. In real use these are the two clicks: Approve or Reject.
type FixedApprover struct {
	decision Decision
	reason   string
}

func (f FixedApprover) Ask(context.Context, string, map[string]interface{}) (Decision, string) {
	return f.decision, f.reason
}

func main() {
	interactive := flag.Bool("i", false, "ask on stdin for non-auto-approved tools")
	approveYes := flag.Bool("y", false, "injected approver says YES to escalations (default: NO)")
	approveAll := flag.Bool("approve-all", false, "policy pre-clears every tool (cline's autoApproveAllToggled)")
	flag.Parse()

	work, err := os.MkdirTemp("", "s04-demo-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(work)
	_ = os.WriteFile(work+"/notes.txt", []byte("hello from the workspace\n"), 0o644)

	// Policy: reads auto-approve anywhere; writes auto-approve only INSIDE the
	// workspace. External writes still need a human. (cline's default posture.)
	policy := &AutoApprovePolicy{
		Workspace:  work,
		ApproveAll: *approveAll,
		Tools: map[string]ToolPolicy{
			"read_file":  {AutoApprove: true, AutoApproveExternal: true},
			"write_file": {AutoApprove: true, AutoApproveExternal: false},
		},
	}

	var human Approver = FixedApprover{decision: DecisionReject, reason: "rejected by injected approver"}
	if *approveYes {
		human = FixedApprover{decision: DecisionApprove, reason: "approved by injected approver"}
	}
	if *interactive {
		human = NewStdinApprover()
	}
	gate := NewApprovalGate(policy, human)

	read := NewReadFileTool(work)
	write := NewWriteFileTool(work)

	// A scripted batch of tool calls: one read (auto), one in-workspace write
	// (auto via path check), one escaping write (policy abstains → human).
	type call struct {
		tool   Tool
		callID string
		params map[string]interface{}
	}
	batch := []call{
		{read, "call_1", map[string]interface{}{"path": "notes.txt"}},
		{write, "call_2", map[string]interface{}{"path": "out.txt", "content": "safe, inside workspace"}},
		{write, "call_3", map[string]interface{}{"path": "../escape.txt", "content": "tries to escape"}},
	}

	fmt.Fprintf(os.Stderr, "[s04] workspace=%s approveAll=%v approveYes=%v interactive=%v\n",
		work, *approveAll, *approveYes, *interactive)
	for _, c := range batch {
		result, decision, ran := gate.Execute(context.Background(), c.tool, c.callID, c.params)
		fmt.Fprintf(os.Stderr, "[gate] %-11s path=%-15q -> %-12s ran=%v\n",
			c.tool.Schema().Name, fmt.Sprint(c.params["path"]), decision, ran)
		fmt.Fprintf(os.Stderr, "       tool_result(%s, is_error=%v): %v\n",
			result.ToolUseID, result.IsError, result.ToolContent)
	}
}
