# s04 · Human-in-the-Loop Approval Gating / 人类在环审批

> The safety model: nothing with side effects runs until it is approved — by a
> human, or by an auto-approve policy that pre-clears the safe cases.
> 安全模型：任何有副作用的操作，必须先获批才能执行——要么人工点"同意"，
> 要么由自动审批策略提前放行那些安全的情况。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s04`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 4 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

s03 dispatched ~27 tools by name and ran every one unconditionally. But a write
or a shell command is *dangerous* — running it without asking is how an agent
deletes your repo. cline's safety model puts a **gate** between "the model asked
for this tool" and `tool.Execute()`: a side-effecting call must be approved
first. An **auto-approve policy** pre-clears the safe cases (read-only tools, or
a write whose path stays inside the workspace) so the human is only asked about
what actually matters. A rejection does **not** abort the task — it round-trips
as a feedback `tool_result` so the model can pick another path.

s03 按名字分发了约 27 个工具，且无条件执行每一个。可写文件、执行命令是**危险**
操作——不问就执行，正是智能体删库的方式。cline 的安全模型在"模型请求工具"和
`tool.Execute()` 之间插入一道**门**：有副作用的调用必须先获批。一个**自动审批
策略**会提前放行安全情况（只读工具，或路径仍在工作区内的写入），这样只有真正
重要的操作才会去问人。被拒绝**不会**中止任务——它会作为一条反馈 `tool_result`
回流给模型，让模型换条路走。

```
tool_use ─▶ ApprovalGate.Execute
                  │
          ┌───────┴─ stage 1: AutoApprovePolicy ── pre-cleared? ──▶ run tool
          │                                                            │
          └─ abstains ─▶ stage 2: human Approver ─ approve? ─yes─▶ run tool
                                            │                         │
                                           no ──▶ feedback tool_result (is_error)
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | Canonical types: `Message` / `ContentBlock` / `ToolSchema` / `Tool` |
| `approval.go` | `Decision` enum, `Approver` interface, `AutoApprovePolicy` (per-tool flags + path check) + `shouldAutoApprove` |
| `gate.go` | `ApprovalGate` — the two-stage gate wrapping a tool execute (policy → human) |
| `tools.go` | `read_file` (read-only, auto-approve) + `write_file` (side-effecting, ask-first); both record `Ran` |
| `main.go` | Offline demo: a scripted batch of tool calls through the gate, with an injected approver |
| `approval_test.go` | 8 tests, fully offline (a `stubApprover`) |

---

## Try it / 动手试一试

Tests need no network and no API key / 测试无需联网、无需 API key：

```bash
cd agents/s04-approval-gating
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Run the demo / 跑演示 (offline, deterministic / 离线、确定性):

```bash
# default: reads + in-workspace writes auto-approve; the escaping write is
# escalated to the injected approver, which REJECTS it (see the feedback).
# 默认：只读 + 工作区内写入自动放行；越界写入升级给注入的审批者，被拒绝。
go run .

go run . -y            # injected approver APPROVES escalations
go run . -approve-all  # policy pre-clears EVERYTHING (autoApproveAllToggled)
go run . -i            # ask Y/N on stdin for the escaping write
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt).
预期输出形态见该文件。

---

## Deliberately omitted / 故意省略

cline's `AutoApprove` also handles multi-root workspaces, per-action token
budgets, notification toggles, and a Plan/Act mode distinction (Appendix A). s04
keeps the *core*: the per-tool allowlist, the local/external path check, the
global approve-all / yolo overrides, and the rejection-as-feedback contract.

cline 的 `AutoApprove` 还处理多根工作区、单次操作的额度、通知开关，以及
Plan/Act 模式之分（见附录 A）。s04 只保留**核心**：按工具的白名单、本地/外部
路径检查、全局 approve-all / yolo 覆盖，以及"拒绝即反馈"这一约定。

See `docs/en/s04-approval-gating.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s04-approval-gating.md`。
