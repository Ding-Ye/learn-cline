---
title: "s04 · Human-in-the-Loop Approval Gating"
chapter: 4
slug: s04-approval-gating
est_read_min: 11
---

# s04 · Human-in-the-Loop Approval Gating

> What this teaches: the approval gate — the rule that nothing with side effects runs until it is approved, by a human or by an auto-approve policy. It gets its own chapter because it is cline's entire safety model, and it is what makes an autonomous agent safe enough to point at a real codebase.

---

## Problem

s03 gave us a registry that dispatches ~27 tools by name and runs each one the moment the model asks. That is exactly what you want for `read_file` — and exactly what you must **never** do for `write_file` or `execute_command`. An LLM is confidently wrong often enough that an agent which writes files and runs shell commands without asking is one hallucination away from `rm -rf` on your repo. Autonomy without a brake is not a feature; it is a liability.

cline's answer is the oldest safety pattern there is: a human in the loop. Before any side-effecting tool runs, the agent stops and asks "approve or reject?". But asking for *every* call — including harmless reads — is so annoying that users would just turn the brake off entirely. So the gate needs a fast path: an **auto-approve policy** that pre-clears the demonstrably safe cases (read-only tools, writes that stay inside the workspace) and only escalates the rest. This chapter builds that gate.

## Solution

The mental model: **insert a gate between "the model asked for a tool" and `tool.Execute()`, and make the gate two-stage.** Stage one is the policy fast path; stage two is the human, consulted only when the policy abstains.

The gate is built from three small pieces:

1. **A `Decision` enum, not a bool.** A call can be `Approve` (a human said yes), `AutoApprove` (the policy pre-cleared it), or `Reject`. Approve and AutoApprove both let the tool run, but keeping them distinct lets us prove "the human was never asked" — the whole point of the fast path.
2. **An `AutoApprovePolicy` with a per-tool allowlist *and* a path check.** A tool flag alone can't decide a write: "edit a file in my repo" and "edit `/etc/hosts`" are different risks. So each tool carries two flags — `AutoApprove` (local) and `AutoApproveExternal` — and the policy resolves them against whether the path stays inside the workspace.
3. **Rejection is feedback, not abort.** A denied call does not kill the task. It returns a `tool_result` with `IsError: true` carrying the user's reason, so the model sees "you were denied; try something else" and keeps going. This is the difference between a safety gate and a kill switch.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  tool_use (name + params + callID)                             │
│        │                                                       │
│        ▼                                                       │
│  ApprovalGate.Execute                                          │
│        │                                                       │
│        ├─ stage 1: AutoApprovePolicy.Ask                      │
│        │     allowlist? + path local/external?                │
│        │        │                                             │
│        │   auto-approve ───────────────────────┐             │
│        │        │ (abstains)                    │             │
│        ▼        ▼                               ▼             │
│  stage 2: human Approver.Ask              tool.Execute        │
│        │                                        │             │
│   approve ──────────────────────────────────────┘            │
│        │                                        │             │
│   reject ──▶ tool_result{is_error:true,    tool_result{      │
│              content: feedback}              content: output} │
└────────────────────────────────────────────────────────────────┘
```

The core of the gate (excerpt from [`agents/s04-approval-gating/gate.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s04-approval-gating/gate.go)):

```go
// decide runs the two-stage approval: policy first, then human.
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

// Execute is the gated tool-execute path — the ONLY way a tool runs in s04.
func (g *ApprovalGate) Execute(ctx context.Context, tool Tool, callID string, params map[string]interface{}) (ContentBlock, Decision, bool) {
	toolName := tool.Schema().Name
	decision, reason := g.decide(ctx, toolName, params)

	if !decision.Allowed() {
		// Rejected: a denial becomes a tool_result fed back to the model (NOT an abort).
		feedback := fmt.Sprintf("The user rejected the %q tool call.", toolName)
		if reason != "" {
			feedback += " Feedback: " + reason
		}
		return ContentBlock{Type: "tool_result", ToolUseID: callID, ToolContent: feedback, IsError: true}, decision, false
	}

	// Approved (by human or policy): run the tool.
	out, err := tool.Execute(ctx, params)
	if err != nil {
		return ContentBlock{Type: "tool_result", ToolUseID: callID, ToolContent: fmt.Sprintf("tool error: %v", err), IsError: true}, decision, false
	}
	return ContentBlock{Type: "tool_result", ToolUseID: callID, ToolContent: out, IsError: false}, decision, true
}
```

**Four non-obvious points**:

1. **Policy abstention is modeled as rejection.** `AutoApprovePolicy.Ask` returns `DecisionReject` when it can't pre-clear — but the gate reads that not as "blocked" but as "escalate to the next approver". Only a *human's* reject (or no human at all) actually blocks. This keeps the two stages composable behind one `Approver` interface.
2. **The path check is what makes a write auto-approvable at all.** Without it you'd face a binary "auto-approve all writes / ask for all writes". The local/external split (ported from upstream's `[local, external]` tuple) lets "writes inside my repo" flow freely while "writes that escape it" still stop for a human.
3. **The `bool` return reports whether `Execute` actually ran.** Tests assert on it directly: a rejected write must leave `ran == false` *and* the tool's own `Ran` field false — proving the side effect never happened, not just that the result looked like an error.
4. **A tool that runs but errors still returns a `tool_result`.** Same convention as s01/s03: the model should *see* the error and recover, so an execution failure is not a Go error bubbling up — it is a `tool_result` with `IsError: true`. Only approval rejections and execution errors share that shape; their `Decision` differs.

## What Changed (vs. s03)

s03's executor ran every dispatched tool unconditionally. s04 routes every call through the gate first:

```diff
- // s03: dispatch resolves the handler and runs it immediately.
- result, err := tool.Execute(ctx, block.Input)
- toolResult := wrapToolResult(block.ID, result, err)
+ // s04: dispatch resolves the handler, then the GATE decides.
+ gate := NewApprovalGate(policy, human) // policy = AutoApprovePolicy, human = Approver
+ toolResult, decision, ran := gate.Execute(ctx, tool, block.ID, block.Input)
+ //                           └─ ran == false when rejected; toolResult carries
+ //                              the feedback (is_error) instead of tool output.
```

The semantic shift: in s03, "the model asked for a tool" and "the tool ran" were the same event. In s04 they are separated by a decision. Execution is now *conditional* — gated by a policy plus, when the policy abstains, a human. The new vocabulary is `Decision` / `Approver` / `AutoApprovePolicy` / `ApprovalGate`, and the new invariant is: **no side-effecting tool runs without an allowing `Decision`.**

## Try It

```bash
cd agents/s04-approval-gating

# Offline, deterministic demo. Default: reads + in-workspace writes auto-approve;
# the escaping write is escalated to the injected approver, which REJECTS it.
go run .

# Injected approver APPROVES escalations → the escaping write now runs.
go run . -y

# Policy pre-clears EVERYTHING (cline's autoApproveAllToggled).
go run . -approve-all

# Tests (8, fully offline — a stubApprover stands in for the human).
go test -v ./...
```

Expected output shape (`go run .`):

```
[s04] workspace=/tmp/s04-demo-XXXX approveAll=false approveYes=false interactive=false
[gate] read_file   path="notes.txt"     -> auto-approve ran=true
       tool_result(call_1, is_error=false): hello from the workspace
[gate] write_file  path="out.txt"       -> auto-approve ran=true
       tool_result(call_2, is_error=false): wrote 22 bytes to out.txt
[gate] write_file  path="../escape.txt" -> reject       ran=false
       tool_result(call_3, is_error=true): The user rejected the "write_file" tool call. Feedback: rejected by injected approver
```

The shape to verify: read and in-workspace write are `auto-approve ran=true` (no prompt), the escaping write is `reject ran=false`, and that rejected call still yields a `tool_result` with `is_error=true` — the feedback the model would see next turn.

## Upstream Source Reading

cline's policy lives in `apps/vscode/src/core/task/tools/autoApprove.ts`. The class `AutoApprove` exposes two methods: `shouldAutoApproveTool` (the per-tool allowlist) and `shouldAutoApproveToolWithPath` (allowlist **and** a workspace-path check). A tool handler calls these *before* running; on a `false`, it falls back to `askApproval` (`task/tools/types/UIHelpers.ts` L56), which boils the webview's rich response down to `response === "yesButtonClicked"`.

```upstream:apps/vscode/src/core/task/tools/autoApprove.ts#L42-L167
// shouldAutoApproveTool returns bool for most tools, and a [local, external]
// tuple for file/command tools — the per-tool allowlist.
shouldAutoApproveTool(toolName: ClineDefaultTool): boolean | [boolean, boolean] {
	if (this.stateManager.getGlobalSettingsKey("yoloModeToggled")) {
		// yolo: every case returns an approve. (s04: AutoApprovePolicy.Yolo)
		switch (toolName) {
			case ClineDefaultTool.FILE_EDIT:
			case ClineDefaultTool.BASH:
				return [true, true]
			case ClineDefaultTool.MCP_USE:
				return true
		}
	}
	if (this.stateManager.getGlobalSettingsKey("autoApproveAllToggled")) {
		// approve-all: same effect, separate toggle. (s04: ApproveAll)
		switch (toolName) {
			case ClineDefaultTool.FILE_EDIT:
				return [true, true]
		}
	}
	const autoApprovalSettings = this.stateManager.getGlobalSettingsKey("autoApprovalSettings")
	switch (toolName) {
		case ClineDefaultTool.FILE_READ:
			// reads: [local, external] pair. (s04: ToolPolicy{AutoApprove, AutoApproveExternal})
			return [autoApprovalSettings.actions.readFiles, autoApprovalSettings.actions.readFilesExternally ?? false]
		case ClineDefaultTool.FILE_EDIT:
			return [autoApprovalSettings.actions.editFiles, autoApprovalSettings.actions.editFilesExternally ?? false]
		case ClineDefaultTool.BASH:
			return [autoApprovalSettings.actions.executeSafeCommands ?? false, autoApprovalSettings.actions.executeAllCommands ?? false]
	}
	return false // DEFAULT: ask a human.
}

// shouldAutoApproveToolWithPath: allowlist AND the path check.
async shouldAutoApproveToolWithPath(blockname: ClineDefaultTool, autoApproveActionpath: string | undefined): Promise<boolean> {
	if (this.stateManager.getGlobalSettingsKey("yoloModeToggled")) return true
	if (this.stateManager.getGlobalSettingsKey("autoApproveAllToggled")) return true

	let isLocalRead = false
	if (autoApproveActionpath) {
		const cwd = await getCwd(getDesktopDir())
		const absolutePath = resolveWorkspacePath(cwd, autoApproveActionpath, "...") as string
		isLocalRead = isLocatedInPath(cwd, absolutePath) // s04: AutoApprovePolicy.isLocal
	}
	const autoApproveResult = this.shouldAutoApproveTool(blockname)
	const [autoApproveLocal, autoApproveExternal] = Array.isArray(autoApproveResult) ? autoApproveResult : [autoApproveResult, false]

	// THE RULE: local needs the local flag; external needs BOTH flags.
	if ((isLocalRead && autoApproveLocal) || (!isLocalRead && autoApproveLocal && autoApproveExternal)) {
		return true
	}
	return false
}
```

**Reading notes**:

- **The `[local, external]` tuple is the design.** Upstream returns a *pair* for file/command tools so the path check can pick the right flag. s04's `ToolPolicy{AutoApprove, AutoApproveExternal}` is that pair given names, and `shouldAutoApprove` applies the same `(isLocal && local) || (external && local && external)` rule.
- **Global overrides short-circuit first.** Both `yoloModeToggled` and `autoApproveAllToggled` are checked *before* the per-tool switch and before any path check. s04 keeps this order exactly: `Yolo` and `ApproveAll` win in `shouldAutoApprove` before the allowlist is consulted.
- **Our gate is sync; upstream's path check is async.** `shouldAutoApproveToolWithPath` is `async` because it resolves the workspace via host RPC (`HostProvider.workspace.getWorkspacePaths`). s04 has the workspace path in hand, so `isLocal` is a pure synchronous `filepath` computation — no host, no cache.
- **The default is "ask".** Upstream's final `return false` (L116) and the safe `isLocalRead = false` when no path is given both mean "when unsure, escalate to a human". s04 ports both: an unlisted tool returns `false`, and an empty `Workspace` treats every path as external.
- **We fuse two methods into one.** Upstream splits the allowlist (`shouldAutoApproveTool`) from the path check (`shouldAutoApproveToolWithPath`) because not every tool has a path. Our toy tools always do, so `AutoApprovePolicy.shouldAutoApprove` does both in one pass — correct for s04, but the split is the more general design.

**Read further**: start at `autoApprove.ts` → `shouldAutoApproveToolWithPath`, follow `askApproval` into `task/tools/types/UIHelpers.ts` (L56), then `ask()` in `task/index.ts` (L661) to see how a denial becomes `formatResponse.toolDenied` feedback rather than an abort. That trace — policy → askApproval → denial-as-feedback — is the real-source map for s04 → Appendix A (the Plan/Act safety model).

---

**Next**: s05 swaps the fake provider for a real streaming SSE client behind a factory, so the gate you built here will finally be deciding on tool calls parsed from a genuine model stream.
