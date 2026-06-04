package main

import (
	"context"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// approval.go — cline's safety model: human-in-the-loop approval gating.
//
// Autonomous file writes and shell commands are dangerous. cline's rule is:
// every side-effecting tool call asks the user "approve / reject?" before it
// runs — UNLESS an auto-approve policy has pre-cleared it (read-only tools, or
// a write whose path stays inside the workspace, or a global "approve all").
//
// This file holds the policy + the gate's vocabulary. The gating wrapper that
// uses them lives in gate.go.
//
// Upstream: apps/vscode/src/core/task/tools/autoApprove.ts
//   - shouldAutoApproveTool          (per-tool allowlist, L42)
//   - shouldAutoApproveToolWithPath  (allowlist AND path check, L122)
// and the interactive ask in task/tools/types/UIHelpers.ts (askApproval L56),
// which boils ask()'s rich response down to a single bool.
// ---------------------------------------------------------------------------

// Decision is the outcome of the gate for one tool call. cline's askApproval
// returns a bare bool (`response === "yesButtonClicked"`); we keep a small enum
// so the gate can distinguish "the human approved" from "the policy auto-
// approved" — both let the tool run, but only the former needed a human.
type Decision int

const (
	// DecisionReject blocks the tool. Its output is NOT run; instead a
	// feedback tool_result is returned to the model (see gate.go).
	DecisionReject Decision = iota
	// DecisionApprove means a human said yes. The tool runs.
	DecisionApprove
	// DecisionAutoApprove means the policy pre-cleared the call; no human was
	// asked. The tool runs. (Kept distinct from DecisionApprove for telemetry
	// and so tests can assert "the human was NOT prompted".)
	DecisionAutoApprove
)

// Allowed reports whether the decision lets the tool execute.
func (d Decision) Allowed() bool { return d == DecisionApprove || d == DecisionAutoApprove }

func (d Decision) String() string {
	switch d {
	case DecisionApprove:
		return "approve"
	case DecisionAutoApprove:
		return "auto-approve"
	default:
		return "reject"
	}
}

// Approver decides a single tool call before execution. This is cline's
// askApproval transport (UIHelpers.ts L35): the gate hands it the tool name and
// parsed params, and gets back a Decision. Implementations:
//   - AutoApprovePolicy  — the policy fast path (this file)
//   - a stdin Y/N reader  — the real human prompt (main.go)
//   - an injected stub    — deterministic decisions for tests
//
// Returning a Reason lets a rejection round-trip useful feedback to the model.
type Approver interface {
	Ask(ctx context.Context, toolName string, params map[string]interface{}) (Decision, string)
}

// ToolPolicy is the per-tool auto-approve configuration. It mirrors the two
// flags cline derives from autoApprovalSettings: one for actions inside the
// workspace and one for actions that escape it (autoApprove.ts L96/L101/L103).
type ToolPolicy struct {
	// AutoApprove pre-clears this tool when its target path is inside the
	// workspace (or when it takes no path at all, like a read).
	AutoApprove bool
	// AutoApproveExternal additionally pre-clears it when the path is OUTSIDE
	// the workspace. cline keeps this separate because "edit a file in my repo"
	// and "edit /etc/hosts" are very different risks.
	AutoApproveExternal bool
}

// AutoApprovePolicy is the per-tool allowlist + global overrides. It is itself
// an Approver: its Ask() returns DecisionAutoApprove when the policy clears the
// call, and DecisionReject otherwise (meaning "policy can't decide — escalate
// to a human", which the gate does by chaining to a second Approver).
type AutoApprovePolicy struct {
	// Tools is the per-tool allowlist (autoApprove.ts switch on toolName).
	Tools map[string]ToolPolicy
	// ApproveAll is cline's autoApproveAllToggled: every tool is pre-cleared,
	// regardless of path (autoApprove.ts L66 / L129).
	ApproveAll bool
	// Yolo is cline's yoloModeToggled — the most permissive switch; here it is
	// a synonym for ApproveAll, kept named separately to match the upstream
	// terminology the docs reference (autoApprove.ts L43 / L126).
	Yolo bool
	// Workspace is the directory a path must stay inside to count as "local".
	// Empty means "treat every path as external" (the safe default).
	Workspace string
}

// PathParamNames are the input keys we treat as a filesystem path when deciding
// local-vs-external. cline reads the path off the specific tool block; we keep a
// small set so the policy stays tool-agnostic.
var PathParamNames = []string{"path", "file", "filename"}

// shouldAutoApprove ports autoApprove.ts shouldAutoApproveToolWithPath: the tool
// must be on the allowlist AND (if it has a path) the path must satisfy the
// local/external flags. Returns the pre-clear bool plus a short reason string.
func (p *AutoApprovePolicy) shouldAutoApprove(toolName string, params map[string]interface{}) (bool, string) {
	// Global overrides win first, exactly like upstream (L43/L66 before the
	// per-tool switch). yolo and approve-all clear everything, path or not.
	if p.Yolo {
		return true, "yolo mode"
	}
	if p.ApproveAll {
		return true, "approve-all mode"
	}

	pol, ok := p.Tools[toolName]
	if !ok || (!pol.AutoApprove && !pol.AutoApproveExternal) {
		// Not on the allowlist (or both flags off): the safe default is false —
		// "ask a human" (autoApprove.ts returns false at L116).
		return false, "not on auto-approve allowlist"
	}

	path, hasPath := extractPath(params)
	if !hasPath {
		// No path to check (e.g. a read with no target, or a pathless tool):
		// the local flag alone decides.
		if pol.AutoApprove {
			return true, "tool auto-approved (no path)"
		}
		return false, "tool requires approval"
	}

	// Mirror autoApprove.ts L163: local paths need AutoApprove; external paths
	// need BOTH AutoApprove and AutoApproveExternal.
	if p.isLocal(path) {
		if pol.AutoApprove {
			return true, "local path auto-approved"
		}
		return false, "local edits require approval"
	}
	if pol.AutoApprove && pol.AutoApproveExternal {
		return true, "external path auto-approved"
	}
	return false, "path outside workspace requires approval"
}

// Ask makes AutoApprovePolicy satisfy Approver. A cleared call becomes
// DecisionAutoApprove; anything else is DecisionReject, which the gate reads as
// "policy abstains — escalate to the next approver".
func (p *AutoApprovePolicy) Ask(_ context.Context, toolName string, params map[string]interface{}) (Decision, string) {
	if ok, reason := p.shouldAutoApprove(toolName, params); ok {
		return DecisionAutoApprove, reason
	}
	return DecisionReject, "policy did not auto-approve"
}

// isLocal reports whether path resolves to somewhere inside the workspace.
// This is the s04 stand-in for cline's isLocatedInPath (autoApprove.ts L150):
// resolve both sides to absolute and check that the relative path doesn't climb
// out with "..".
func (p *AutoApprovePolicy) isLocal(path string) bool {
	if p.Workspace == "" {
		return false // no workspace configured → nothing is "local"
	}
	absWS, err := filepath.Abs(p.Workspace)
	if err != nil {
		return false
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(absWS, target)
	}
	absTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absWS, absTarget)
	if err != nil {
		return false
	}
	// Inside iff the relative path is "." or doesn't start with "..".
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// extractPath pulls the first path-like param out of the tool input.
func extractPath(params map[string]interface{}) (string, bool) {
	for _, k := range PathParamNames {
		if v, ok := params[k].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}
