package main

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// gate.go — the approval gate: the wrapper that sits between "the model asked
// for a tool" and "tool.Execute() runs".
//
// This is the s04 distillation of cline's ToolExecutor flow: in upstream a tool
// handler first calls shouldAutoApproveTool(...); if that clears, it just runs;
// otherwise it calls askApproval(...) and only runs on a yes. A rejection does
// NOT abort the task — it pushes a feedback tool_result so the model can react
// (see ToolExecutor.ts and formatResponse.toolDenied).
//
// We make that two-step explicit with an ApprovalGate holding two Approvers:
//   1. policy — the AutoApprove fast path (read-only / in-workspace / yolo)
//   2. human  — consulted only when the policy abstains
// ---------------------------------------------------------------------------

// ApprovalGate gates one tool execution. Construct it once with a policy and a
// human approver, then call Execute for each tool call.
type ApprovalGate struct {
	// policy is the auto-approve fast path. If nil, every call falls through to
	// the human (the safe default: gate nothing automatically).
	policy *AutoApprovePolicy
	// human is consulted when the policy does not pre-clear the call. If nil,
	// a non-auto-approved call is rejected outright (headless, no one to ask).
	human Approver
}

// NewApprovalGate wires the two-stage gate.
func NewApprovalGate(policy *AutoApprovePolicy, human Approver) *ApprovalGate {
	return &ApprovalGate{policy: policy, human: human}
}

// decide runs the two-stage approval: policy first, then human. It returns the
// final Decision and a reason. This mirrors the upstream order — auto-approve is
// checked BEFORE bothering the user (autoApprove.ts is consulted first in every
// handler, askApproval second).
func (g *ApprovalGate) decide(ctx context.Context, toolName string, params map[string]interface{}) (Decision, string) {
	// Stage 1: policy fast path.
	if g.policy != nil {
		if d, reason := g.policy.Ask(ctx, toolName, params); d == DecisionAutoApprove {
			return d, reason
		}
	}
	// Stage 2: escalate to the human (or reject if there is none).
	if g.human == nil {
		return DecisionReject, "no approver available; auto-reject"
	}
	return g.human.Ask(ctx, toolName, params)
}

// Execute is the gated tool-execute path. It is the ONLY way a tool should be
// run in s04. Behaviour:
//   - decision allows  → run tool.Execute and wrap the output as a tool_result
//   - decision rejects → DO NOT run; return a feedback tool_result (IsError)
//
// callID is the tool_use id from the assistant turn; it is echoed onto the
// tool_result so the model can match request to result (the s03 contract).
// The returned ContentBlock is always a "tool_result"; the bool reports whether
// tool.Execute actually ran (false on rejection or tool error before output).
func (g *ApprovalGate) Execute(
	ctx context.Context,
	tool Tool,
	callID string,
	params map[string]interface{},
) (ContentBlock, Decision, bool) {
	toolName := tool.Schema().Name
	decision, reason := g.decide(ctx, toolName, params)

	if !decision.Allowed() {
		// Rejected. cline turns a denial into a tool_result fed back to the
		// model (NOT an abort), so the model can apologise / pick another path.
		feedback := fmt.Sprintf(
			"The user rejected the %q tool call.", toolName)
		if reason != "" {
			feedback += " Feedback: " + reason
		}
		return ContentBlock{
			Type:        "tool_result",
			ToolUseID:   callID,
			ToolContent: feedback,
			IsError:     true,
		}, decision, false
	}

	// Approved (by human or policy): run the tool.
	out, err := tool.Execute(ctx, params)
	if err != nil {
		// A tool that ran but failed still returns a tool_result so the model
		// SEES the error and can recover (same convention as s01/s03).
		return ContentBlock{
			Type:        "tool_result",
			ToolUseID:   callID,
			ToolContent: fmt.Sprintf("tool error: %v", err),
			IsError:     true,
		}, decision, false
	}
	return ContentBlock{
		Type:        "tool_result",
		ToolUseID:   callID,
		ToolContent: out,
		IsError:     false,
	}, decision, true
}
