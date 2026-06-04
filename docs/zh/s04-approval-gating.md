---
title: "s04 · 人类在环审批"
chapter: 4
slug: s04-approval-gating
est_read_min: 11
---

# s04 · 人类在环审批

> 教什么：审批门——"任何有副作用的操作，必须先获批（由人工或自动审批策略）才能执行"这条规则。它单独成章，是因为它就是 cline 的**整个安全模型**，也正是它让一个自主智能体安全到可以指向真实代码库。

---

## Problem

s03 给了我们一个注册表，按名字分发约 27 个工具，模型一请求就立刻执行。对 `read_file` 来说这正是你想要的——但对 `write_file` 或 `execute_command` 来说，这是你**绝对不能**做的。大模型自信地犯错的频率高到：一个会写文件、执行命令却不问的智能体，离对你的仓库 `rm -rf` 只差一次幻觉。没有刹车的自主性不是特性，是隐患。

cline 的答案是最古老的安全模式：人类在环。在任何有副作用的工具运行前，智能体停下来问"同意还是拒绝？"。但**每一次**调用都问——包括无害的读取——会烦到用户干脆把刹车整个关掉。所以这道门需要一条快车道：一个**自动审批策略**，提前放行那些明显安全的情况（只读工具、留在工作区内的写入），只把其余的升级给人。本章就构建这道门。

## Solution

心智模型：**在"模型请求工具"和 `tool.Execute()` 之间插入一道门，并让这道门分两级。** 第一级是策略快车道；第二级是人，只在策略弃权时才咨询。

这道门由三个小部件构成：

1. **用 `Decision` 枚举，而非 bool。** 一次调用可以是 `Approve`（人工同意）、`AutoApprove`（策略提前放行）或 `Reject`。Approve 和 AutoApprove 都让工具运行，但保持二者可区分，才能证明"压根没问过人"——这正是快车道的全部意义。
2. **`AutoApprovePolicy` 同时带按工具白名单*和*路径检查。** 单凭工具开关无法决定一次写入："改我仓库里的文件"和"改 `/etc/hosts`"是不同的风险。所以每个工具带两个开关——`AutoApprove`（本地）和 `AutoApproveExternal`（外部）——策略再结合"路径是否在工作区内"来裁决。
3. **拒绝是反馈，不是中止。** 被拒的调用不会杀死任务。它返回一个 `IsError: true` 的 `tool_result`，携带用户的理由，于是模型看到"你被拒了，换个法子"并继续。这就是安全门和急停开关的区别。

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  tool_use (name + params + callID)                             │
│        │                                                       │
│        ▼                                                       │
│  ApprovalGate.Execute                                          │
│        │                                                       │
│        ├─ 第 1 级: AutoApprovePolicy.Ask                      │
│        │     在白名单? + 路径 本地/外部?                       │
│        │        │                                             │
│        │   auto-approve ───────────────────────┐             │
│        │        │ (弃权)                         │             │
│        ▼        ▼                               ▼             │
│  第 2 级: human Approver.Ask              tool.Execute        │
│        │                                        │             │
│   approve ──────────────────────────────────────┘            │
│        │                                        │             │
│   reject ──▶ tool_result{is_error:true,    tool_result{      │
│              content: 反馈}                  content: 输出}    │
└────────────────────────────────────────────────────────────────┘
```

门的核心（节选自 [`agents/s04-approval-gating/gate.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s04-approval-gating/gate.go)）：

```go
// decide 跑两级审批：先策略，再人工。
func (g *ApprovalGate) decide(ctx context.Context, toolName string, params map[string]interface{}) (Decision, string) {
	// 第 1 级：策略快车道。
	if g.policy != nil {
		if d, reason := g.policy.Ask(ctx, toolName, params); d == DecisionAutoApprove {
			return d, reason
		}
	}
	// 第 2 级：升级给人（没有人就拒绝）。
	if g.human == nil {
		return DecisionReject, "no approver available; auto-reject"
	}
	return g.human.Ask(ctx, toolName, params)
}

// Execute 是受门控的工具执行路径——s04 中工具运行的唯一入口。
func (g *ApprovalGate) Execute(ctx context.Context, tool Tool, callID string, params map[string]interface{}) (ContentBlock, Decision, bool) {
	toolName := tool.Schema().Name
	decision, reason := g.decide(ctx, toolName, params)

	if !decision.Allowed() {
		// 被拒：拒绝变成回流给模型的 tool_result（不是中止）。
		feedback := fmt.Sprintf("The user rejected the %q tool call.", toolName)
		if reason != "" {
			feedback += " Feedback: " + reason
		}
		return ContentBlock{Type: "tool_result", ToolUseID: callID, ToolContent: feedback, IsError: true}, decision, false
	}

	// 已批准（人工或策略）：运行工具。
	out, err := tool.Execute(ctx, params)
	if err != nil {
		return ContentBlock{Type: "tool_result", ToolUseID: callID, ToolContent: fmt.Sprintf("tool error: %v", err), IsError: true}, decision, false
	}
	return ContentBlock{Type: "tool_result", ToolUseID: callID, ToolContent: out, IsError: false}, decision, true
}
```

**4 个非显然之处**：

1. **策略弃权被建模成"拒绝"。** `AutoApprovePolicy.Ask` 无法放行时返回 `DecisionReject`——但门把它读作"升级给下一个审批者"，而非"阻断"。只有*人工*的拒绝（或根本没有人）才真正阻断。这让两级能组合在同一个 `Approver` 接口背后。
2. **正是路径检查让一次写入变得可自动放行。** 没有它，你只能在"放行所有写入 / 询问所有写入"之间二选一。本地/外部的拆分（移植自上游的 `[local, external]` 元组）让"我仓库内的写入"畅通，而"逃出仓库的写入"仍为人停下。
3. **返回的 `bool` 报告 `Execute` 是否真的运行了。** 测试直接对它断言：被拒的写入必须留下 `ran == false`，*且*工具自身的 `Ran` 字段为 false——证明副作用从未发生，而不只是结果看起来像个错误。
4. **运行了但出错的工具仍返回 `tool_result`。** 与 s01/s03 同一约定：模型应当*看见*错误并恢复，所以执行失败不是冒泡上来的 Go error——它是一个 `IsError: true` 的 `tool_result`。只有审批拒绝和执行错误共享这个形态；它们的 `Decision` 不同。

## What Changed (vs. s03)

s03 的执行器无条件运行每个被分发的工具。s04 让每次调用先穿过门：

```diff
- // s03：分发解析出 handler 后立刻运行。
- result, err := tool.Execute(ctx, block.Input)
- toolResult := wrapToolResult(block.ID, result, err)
+ // s04：分发解析出 handler，然后由门来裁决。
+ gate := NewApprovalGate(policy, human) // policy = AutoApprovePolicy, human = Approver
+ toolResult, decision, ran := gate.Execute(ctx, tool, block.ID, block.Input)
+ //                           └─ 被拒时 ran == false；toolResult 携带反馈
+ //                              (is_error) 而非工具输出。
```

语义上的转变：在 s03，"模型请求工具"和"工具运行了"是同一个事件。在 s04，它们被一次裁决隔开。执行现在是*有条件的*——由策略门控，策略弃权时再由人门控。新词汇是 `Decision` / `Approver` / `AutoApprovePolicy` / `ApprovalGate`，新不变量是：**没有一个允许性 `Decision`，任何有副作用的工具都不会运行。**

## Try It

```bash
cd agents/s04-approval-gating

# 离线、确定性演示。默认：只读 + 工作区内写入自动放行；
# 越界写入升级给注入的审批者，被它拒绝。
go run .

# 注入的审批者对升级说 YES → 越界写入此时会运行。
go run . -y

# 策略提前放行所有工具（cline 的 autoApproveAllToggled）。
go run . -approve-all

# 测试（8 个，全离线——一个 stubApprover 充当人）。
go test -v ./...
```

期望输出形态（`go run .`）：

```
[s04] workspace=/tmp/s04-demo-XXXX approveAll=false approveYes=false interactive=false
[gate] read_file   path="notes.txt"     -> auto-approve ran=true
       tool_result(call_1, is_error=false): hello from the workspace
[gate] write_file  path="out.txt"       -> auto-approve ran=true
       tool_result(call_2, is_error=false): wrote 22 bytes to out.txt
[gate] write_file  path="../escape.txt" -> reject       ran=false
       tool_result(call_3, is_error=true): The user rejected the "write_file" tool call. Feedback: rejected by injected approver
```

要验证的形态：读取与工作区内写入是 `auto-approve ran=true`（不询问），越界写入是 `reject ran=false`，且被拒的那次调用仍产出一个 `is_error=true` 的 `tool_result`——这就是模型下一轮会看到的反馈。

## Upstream Source Reading

cline 的策略在 `apps/vscode/src/core/task/tools/autoApprove.ts`。`AutoApprove` 类暴露两个方法：`shouldAutoApproveTool`（按工具白名单）和 `shouldAutoApproveToolWithPath`（白名单**加**工作区路径检查）。工具 handler 在运行*前*调用它们；返回 `false` 时，回退到 `askApproval`（`task/tools/types/UIHelpers.ts` L56），后者把 webview 的丰富响应压成 `response === "yesButtonClicked"`。

```upstream:apps/vscode/src/core/task/tools/autoApprove.ts#L42-L167
// shouldAutoApproveTool 对多数工具返回 bool，对文件/命令工具返回
// [local, external] 元组——这就是按工具白名单。
shouldAutoApproveTool(toolName: ClineDefaultTool): boolean | [boolean, boolean] {
	if (this.stateManager.getGlobalSettingsKey("yoloModeToggled")) {
		// yolo：每个 case 都返回放行。(s04: AutoApprovePolicy.Yolo)
		switch (toolName) {
			case ClineDefaultTool.FILE_EDIT:
			case ClineDefaultTool.BASH:
				return [true, true]
			case ClineDefaultTool.MCP_USE:
				return true
		}
	}
	if (this.stateManager.getGlobalSettingsKey("autoApproveAllToggled")) {
		// approve-all：效果相同，独立开关。(s04: ApproveAll)
		switch (toolName) {
			case ClineDefaultTool.FILE_EDIT:
				return [true, true]
		}
	}
	const autoApprovalSettings = this.stateManager.getGlobalSettingsKey("autoApprovalSettings")
	switch (toolName) {
		case ClineDefaultTool.FILE_READ:
			// 读取：[local, external] 对。(s04: ToolPolicy{AutoApprove, AutoApproveExternal})
			return [autoApprovalSettings.actions.readFiles, autoApprovalSettings.actions.readFilesExternally ?? false]
		case ClineDefaultTool.FILE_EDIT:
			return [autoApprovalSettings.actions.editFiles, autoApprovalSettings.actions.editFilesExternally ?? false]
		case ClineDefaultTool.BASH:
			return [autoApprovalSettings.actions.executeSafeCommands ?? false, autoApprovalSettings.actions.executeAllCommands ?? false]
	}
	return false // 默认：问人。
}

// shouldAutoApproveToolWithPath：白名单 AND 路径检查。
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

	// 规则：本地需要本地开关；外部需要两个开关都开。
	if ((isLocalRead && autoApproveLocal) || (!isLocalRead && autoApproveLocal && autoApproveExternal)) {
		return true
	}
	return false
}
```

**对照阅读要点**：

- **`[local, external]` 元组就是设计核心。** 上游对文件/命令工具返回一个*对*，好让路径检查挑出正确的开关。s04 的 `ToolPolicy{AutoApprove, AutoApproveExternal}` 就是给这个对起了名字，`shouldAutoApprove` 应用同一条 `(isLocal && local) || (external && local && external)` 规则。
- **全局覆盖先短路。** `yoloModeToggled` 和 `autoApproveAllToggled` 都在按工具 switch *之前*、任何路径检查之前被检查。s04 完全保留这个顺序：`Yolo` 和 `ApproveAll` 在 `shouldAutoApprove` 里先于白名单生效。
- **我们的门是同步的；上游的路径检查是异步的。** `shouldAutoApproveToolWithPath` 是 `async`，因为它通过 host RPC（`HostProvider.workspace.getWorkspacePaths`）解析工作区。s04 手里已有工作区路径，所以 `isLocal` 是纯同步的 `filepath` 计算——无 host、无缓存。
- **默认是"问"。** 上游末尾的 `return false`（L116），以及未给路径时安全的 `isLocalRead = false`，都意味着"不确定时升级给人"。s04 两者都移植：未列入的工具返回 `false`，空 `Workspace` 把每个路径都当作外部。
- **我们把两个方法融成一个。** 上游把白名单（`shouldAutoApproveTool`）和路径检查（`shouldAutoApproveToolWithPath`）拆开，因为并非每个工具都有路径。我们的玩具工具总是有，所以 `AutoApprovePolicy.shouldAutoApprove` 一趟做完两件事——对 s04 是正确的，但拆开才是更通用的设计。

**想读更多**：从 `autoApprove.ts` 的 `shouldAutoApproveToolWithPath` 入手，跟着 `askApproval` 进 `task/tools/types/UIHelpers.ts`（L56），再到 `task/index.ts` 的 `ask()`（L661），看一次拒绝如何变成 `formatResponse.toolDenied` 反馈而非中止。这条线——策略 → askApproval → 拒绝即反馈——就是 s04 → 附录 A（Plan/Act 安全模型）的真实代码地图。

---

**下一节预告**：s05 把假供应商换成一个工厂背后的真实 SSE 流式客户端，于是你在这里构建的门，终于会对从真实模型流里解析出的工具调用做裁决。
