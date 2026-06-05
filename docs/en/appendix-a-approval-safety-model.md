---
title: "Appendix A · Human-in-the-Loop Approval as cline's Safety Model"
appendix: A
slug: appendix-a-approval-safety-model
est_read_min: 12
---

# Appendix A · Human-in-the-Loop Approval as cline's Safety Model

> Upstream: [cline](https://github.com/cline/cline) @ `a209825116dca469c80af4be53989638dd329f38`. Every line number below is pinned to that commit.

## What this appendix is about

The ten chapters build mechanisms — a loop, a parser, a registry, a diff engine. This appendix steps back and names the *idea* that ties the dangerous half of them together: **a coding agent that edits files and runs shell commands is only safe because a human (or an explicit policy) approves each side effect before it happens.** Approval is not a feature bolted onto cline; it is the load-bearing wall the whole product leans on.

s04 already built the gate as code. Here we explain the *why* and the *when*: why an autonomous agent needs a human in the loop at all, how the per-tool auto-approve whitelist keeps that gate from being so annoying users disable it, and how **Plan mode vs Act mode** frames the question of *when* approval even matters. The goal is that after reading you can look at any agent and ask the right question: "what stops this thing from running `rm -rf` on a hallucination?" For cline, the answer is this appendix.

This is a conceptual companion to [s04](./s04-approval-gating.md). Read s04 for the runnable Go gate; read this for the model behind it.

## The mental model

Start from the threat. An LLM is confidently wrong often enough that an agent which writes files and runs commands without asking is one bad token away from deleting your repo or `curl | sh`-ing something hostile. Autonomy without a brake is not a feature — it is a liability. So cline inserts a gate between *"the model asked for a tool"* and *"the tool runs"*, and the gate is decided by a human unless a policy has pre-cleared the call.

```text
        the model asked for a tool
                  │
                  ▼
        ┌───────────────────────┐
        │   is it side-effecting?│
        └───────────────────────┘
           │ no              │ yes
           ▼                 ▼
       run it          ┌──────────────────────┐
                       │ auto-approve policy?  │
                       │ (per-tool whitelist + │
                       │  workspace-path check)│
                       └──────────────────────┘
                          │ cleared      │ abstains
                          ▼              ▼
                       run it     ┌──────────────┐
                                  │  ask a HUMAN │
                                  └──────────────┘
                                   │ approve  │ reject
                                   ▼          ▼
                                run it   tool_result(is_error)
                                          fed back to the model
```

Four ideas hold this up:

- **Per-tool gating, not all-or-nothing.** Reads are safe; writes and shell commands are not. cline classifies every tool and gates only the dangerous ones, so harmless calls (`read_file`, `list_files`, `search_files`) never interrupt you.
- **Auto-approve whitelists as the escape valve.** If you had to click "approve" for *every* call, you'd turn approval off and lose all of it. So cline lets you pre-clear categories — "auto-approve reads", "auto-approve edits inside my workspace" — with global overrides (`autoApproveAllToggled`, `yoloModeToggled`) for users who want full autonomy. The whitelist is what makes the gate survivable in daily use.
- **The path check refines a binary into a gradient.** "Edit a file in my repo" and "edit `/etc/hosts`" are different risks. cline carries a `[local, external]` pair per file/command tool: a write inside the workspace can auto-approve while a write that *escapes* it still stops for a human. Safety scales with blast radius.
- **Rejection is feedback, not abort.** A denied call does not kill the task. It returns a `tool_result` with `is_error: true` carrying the user's reason, so the model sees "you were denied — try something else" and keeps going. This is the difference between a safety gate and a kill switch.

**Plan vs Act mode** is the *temporal* frame around all of this. cline runs in two modes you toggle at will:

- **Plan mode** — the agent reads the codebase, asks clarifying questions, and proposes an approach. In strict plan mode, file-modification tools are *refused outright* before approval is even reached: the model is told the tool "is not available in PLAN MODE." You align on intent first, with zero risk of an edit slipping through.
- **Act mode** — the agent executes the agreed plan, and *now* the approval gate does its work, clearing safe calls and escalating risky ones.

The two modes split a task into "decide what to do" (cheap to get wrong, fully reversible) and "do it" (expensive to get wrong, gated). Approval protects the second phase; Plan mode makes sure you only enter the second phase deliberately.

## How it shows up in upstream

The policy and the gate live in two files. The whitelist is [`apps/vscode/src/core/task/tools/autoApprove.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts); the dispatch + mode enforcement is [`apps/vscode/src/core/task/ToolExecutor.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts).

- **The per-tool whitelist is one big switch.** `AutoApprove.shouldAutoApproveTool` ([autoApprove.ts L42-L117](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts#L42-L117)) maps each `ClineDefaultTool` to its policy: reads/lists/search return a `[local, external]` pair from `autoApprovalSettings.actions.readFiles` (L91-L96), file edits from `editFiles` (L97-L101), `BASH` from `executeSafeCommands`/`executeAllCommands` (L102-L106), and the final `return false` (L116) means **"when unsure, ask a human."**
- **The path check is a separate, stricter method.** `shouldAutoApproveToolWithPath` ([autoApprove.ts L122-L167](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts#L122-L167)) resolves whether the target path is inside the workspace (`isLocatedInPath`, L150) and then applies **the rule** at L163: `(isLocalRead && autoApproveLocal) || (!isLocalRead && autoApproveLocal && autoApproveExternal)` — local needs the local flag, external needs *both*.
- **Global overrides short-circuit first.** Both `yoloModeToggled` (L43, L126) and `autoApproveAllToggled` (L66, L129) are checked *before* the per-tool switch and *before* any path check, so "full autonomy" wins immediately when the user opts in.
- **`ToolExecutor` owns the policy instance.** It constructs `new AutoApprove(this.stateManager)` ([ToolExecutor.ts L126](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts#L126)) and exposes thin wrappers `shouldAutoApproveTool` (L48-L50) and `shouldAutoApproveToolWithPath` (L52-L57) that handlers call before running anything.
- **Plan mode is enforced as a refusal, before approval.** In `ToolExecutor.execute` ([ToolExecutor.ts L342-L357](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts#L342-L357)), if `strictPlanModeEnabled` and `mode === "plan"` and `isPlanModeToolRestricted(block.name)` (L388-L390), the tool returns a hard error — *"is not available in PLAN MODE"* — and never reaches the gate.
- **The approval transport is `ask`/`say`.** Handlers consult the policy, and on a `false` fall back to `askApproval` ([UIHelpers.ts L56-L59](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/types/UIHelpers.ts#L56-L59)), which calls `Task.ask` ([task/index.ts L661](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts#L661)) and reduces the webview's rich answer to `response === "yesButtonClicked"`. Status is streamed back via `Task.say` ([task/index.ts L826](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts#L826)).
- **A real handler shows the two-stage flow.** `WriteToFileToolHandler` calls `shouldAutoApproveToolWithPath` first ([WriteToFileToolHandler.ts L74](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers/WriteToFileToolHandler.ts#L74), again at L205) and only `ask`s the human when it returns false (L79, L243); a rejection sets `taskState.didRejectTool = true` (L276) so the loop turns the denial into feedback.
- **Plan/Act are first-class tools and a runtime toggle.** `PlanModeRespondHandler` (`name = ClineDefaultTool.PLAN_MODE`, [PlanModeRespondHandler.ts L15](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers/PlanModeRespondHandler.ts#L15)) and `ActModeRespondHandler` ([ActModeRespondHandler.ts L10](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers/ActModeRespondHandler.ts#L10)) let the model speak in each mode; the user flips modes through `togglePlanActModeProto` ([togglePlanActModeProto.ts L13-L21](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/controller/state/togglePlanActModeProto.ts#L13-L21)).

## How our mini reflects it

[`agents/s04-approval-gating`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s04-approval-gating) ports the safety model down to a runnable, offline Go gate. The mapping is deliberate:

| Upstream idea | Our mini |
|---------------|----------|
| `shouldAutoApproveTool` whitelist | `AutoApprovePolicy` with a per-tool table (`approval.go`) |
| `[local, external]` tuple | `ToolPolicy{AutoApprove, AutoApproveExternal}` |
| `shouldAutoApproveToolWithPath` path check | `AutoApprovePolicy.shouldAutoApprove` fusing allowlist + `isLocal` |
| `askApproval` → `Task.ask` | the `Approver` interface (a `stubApprover` in tests, stdin Y/N in `main.go`) |
| denial → `didRejectTool` → feedback | `gate.Execute` returns a `tool_result{is_error:true}` instead of aborting |
| `yoloModeToggled` / `autoApproveAllToggled` | the `-y` and `-approve-all` flags on the demo |

The one thing s04 *doesn't* port is Plan/Act mode — it would need a second mode flag and the strict-mode refusal path, which is conceptual rather than mechanical. That gap is exactly what this appendix fills: s04 gives you the gate; this gives you the mode framing around it. Run `go run .` in that directory to watch a read and an in-workspace write auto-approve while an escaping write is rejected and round-tripped as feedback — the whole safety model in fifteen lines of output.

One anti-pattern to carry away (called out in the research dossier): cline's desktop approval can block *indefinitely* if the decision never arrives — there is no timeout on the wait. A production gate should bound the wait and fail safe (treat a timeout as a reject), not hang the agent.

## Further reading

- [s04 · Human-in-the-Loop Approval Gating](./s04-approval-gating.md) — the runnable gate this appendix explains.
- [s03 · Tool Registry & Execution](./s03-tool-registry-execution.md) — the unconditional executor that s04 wraps with a gate.
- [Appendix B · Upstream map](./appendix-b-upstream-map.md) — the file-by-file reading order for the real source.
- Upstream: [`task/tools/autoApprove.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts), [`task/ToolExecutor.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts), and the Plan/Act handlers under [`task/tools/handlers/`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers).
