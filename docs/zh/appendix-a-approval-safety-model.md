---
title: "附录 A · 人类在环审批：cline 的安全模型"
appendix: A
slug: appendix-a-approval-safety-model
est_read_min: 12
---

# 附录 A · 人类在环审批：cline 的安全模型

> 上游：[cline](https://github.com/cline/cline) @ `a209825116dca469c80af4be53989638dd329f38`。下文每一个行号都钉死在这个 commit 上。

## 这份附录讲什么

十章正文构建的是机制——一个循环、一个解析器、一个注册表、一个差异引擎。这份附录退一步，给把其中"危险的那一半"串起来的那个**理念**起个名字：**一个会改文件、会跑 shell 命令的编码智能体，之所以安全，仅仅因为每一个副作用在发生之前都由人工（或一条显式策略）批准过。** 审批不是后来栓到 cline 上的一个功能；它是整个产品赖以支撑的承重墙。

s04 已经把这道门写成了代码。这里我们解释"为什么"和"什么时候"：为什么一个自主智能体根本就需要人类在环；每工具的自动批准白名单如何让这道门不至于烦到用户把它关掉；以及 **Plan 模式与 Act 模式**如何框定"审批到底在什么时候才有意义"这个问题。目标是：读完之后，你看到任何一个智能体都能问出那个对的问题——"是什么阻止了这玩意儿因为一次幻觉就 `rm -rf` 你的仓库？"对 cline 而言，答案就是这份附录。

这是 [s04](./s04-approval-gating.md) 的概念伴读。想要能跑的 Go 门控，读 s04；想要它背后的模型，读这里。

## 心智模型

从威胁出发。大模型自信地犯错的频率高到这种地步：一个不问就写文件、跑命令的智能体，离删掉你的仓库、或 `curl | sh` 一个恶意脚本，只差一个糟糕的 token。没有刹车的自主性不是特性，是隐患。所以 cline 在*"模型请求了一个工具"*和*"工具运行"*之间插入一道门，而这道门由人类裁决——除非有一条策略已经提前放行了这次调用。

```text
        模型请求了一个工具
                  │
                  ▼
        ┌───────────────────────┐
        │     它有副作用吗？      │
        └───────────────────────┘
           │ 没有            │ 有
           ▼                 ▼
        直接运行       ┌──────────────────────┐
                       │   自动批准策略命中？   │
                       │  （每工具白名单 +     │
                       │   工作区路径检查）     │
                       └──────────────────────┘
                          │ 放行         │ 弃权
                          ▼              ▼
                       直接运行     ┌──────────────┐
                                    │   询问人类    │
                                    └──────────────┘
                                     │ 同意    │ 拒绝
                                     ▼         ▼
                                  运行   tool_result(is_error)
                                          回喂给模型
```

支撑它的有四个理念：

- **逐工具门控，而非全有或全无。** 读取是安全的；写入和 shell 命令不是。cline 给每个工具分类，只对危险的那些设门，所以无害的调用（`read_file`、`list_files`、`search_files`）从不打断你。
- **自动批准白名单是泄压阀。** 如果你得为*每一次*调用点"同意"，你会干脆把审批关掉，从而失去一切保护。所以 cline 让你按类别提前放行——"自动批准读取"、"自动批准工作区内的编辑"——再加上给想要完全自主的用户的全局开关（`autoApproveAllToggled`、`yoloModeToggled`）。正是白名单让这道门在日常使用中活得下去。
- **路径检查把二元开关变成渐变。** "改我仓库里的文件"和"改 `/etc/hosts`"是不同的风险。cline 给每个文件/命令类工具带一个 `[local, external]` 二元组：工作区内的写入可以自动批准，而*逃出*工作区的写入仍然停下来问人。安全性随爆炸半径而缩放。
- **拒绝是反馈，不是中止。** 一次被拒的调用不会杀死任务。它返回一个 `is_error: true` 的 `tool_result`，带上用户的理由，于是模型看到"你被拒了——换个办法"，然后继续往下走。这就是安全门和急停开关的区别。

**Plan 模式 vs Act 模式**是套在这一切外面的*时间*框架。cline 跑在两个你可以随时切换的模式里：

- **Plan 模式**——智能体读代码库、问澄清问题、提出方案。在严格 plan 模式下，文件修改类工具在到达审批之前就被*直接拒绝*：模型被告知该工具"在 PLAN MODE 下不可用"。你先对齐意图，零风险——没有任何编辑能溜过去。
- **Act 模式**——智能体执行商定的方案，*现在*审批门才开始干活，放行安全调用、升级危险调用。

这两个模式把一个任务切成"决定做什么"（弄错了代价低、完全可逆）和"动手做"（弄错了代价高、有门控）。审批保护第二个阶段；Plan 模式确保你只在深思熟虑后才进入第二个阶段。

## 在上游里的位置

策略和门控分在两个文件里。白名单在 [`apps/vscode/src/core/task/tools/autoApprove.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts)；分发 + 模式强制在 [`apps/vscode/src/core/task/ToolExecutor.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts)。

- **每工具白名单是一个大 switch。** `AutoApprove.shouldAutoApproveTool`（[autoApprove.ts L42-L117](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts#L42-L117)）把每个 `ClineDefaultTool` 映射到它的策略：读取/列目录/搜索从 `autoApprovalSettings.actions.readFiles`（L91-L96）返回一个 `[local, external]` 二元组，文件编辑从 `editFiles`（L97-L101），`BASH` 从 `executeSafeCommands`/`executeAllCommands`（L102-L106），而最后的 `return false`（L116）意味着**"拿不准时，问人。"**
- **路径检查是另一个、更严格的方法。** `shouldAutoApproveToolWithPath`（[autoApprove.ts L122-L167](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts#L122-L167)）先判定目标路径是否在工作区内（`isLocatedInPath`，L150），然后在 L163 套用**那条规则**：`(isLocalRead && autoApproveLocal) || (!isLocalRead && autoApproveLocal && autoApproveExternal)`——本地只需本地标志，外部需要*两个*标志都有。
- **全局开关最先短路。** `yoloModeToggled`（L43、L126）和 `autoApproveAllToggled`（L66、L129）都在每工具 switch *之前*、在任何路径检查*之前*被检查，所以用户一旦选择"完全自主"就立刻生效。
- **`ToolExecutor` 持有策略实例。** 它构造 `new AutoApprove(this.stateManager)`（[ToolExecutor.ts L126](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts#L126)），并暴露薄封装 `shouldAutoApproveTool`（L48-L50）和 `shouldAutoApproveToolWithPath`（L52-L57），供 handler 在运行任何东西之前调用。
- **Plan 模式作为"拒绝"被强制，在审批之前。** 在 `ToolExecutor.execute`（[ToolExecutor.ts L342-L357](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts#L342-L357)）里，若 `strictPlanModeEnabled` 且 `mode === "plan"` 且 `isPlanModeToolRestricted(block.name)`（L388-L390），工具返回一个硬错误——*"在 PLAN MODE 下不可用"*——根本到不了门控。
- **审批的传输通道是 `ask`/`say`。** handler 咨询策略，在 `false` 时回退到 `askApproval`（[UIHelpers.ts L56-L59](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/types/UIHelpers.ts#L56-L59)），它调用 `Task.ask`（[task/index.ts L661](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts#L661)）并把 webview 的丰富回答收敛成 `response === "yesButtonClicked"`。状态通过 `Task.say`（[task/index.ts L826](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts#L826)）流式回传。
- **一个真实 handler 展示两段式流程。** `WriteToFileToolHandler` 先调 `shouldAutoApproveToolWithPath`（[WriteToFileToolHandler.ts L74](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers/WriteToFileToolHandler.ts#L74)，L205 再次），只在它返回 false 时才 `ask` 人（L79、L243）；一次拒绝会设 `taskState.didRejectTool = true`（L276），于是循环把这次否决变成反馈。
- **Plan/Act 是一等工具，也是运行时开关。** `PlanModeRespondHandler`（`name = ClineDefaultTool.PLAN_MODE`，[PlanModeRespondHandler.ts L15](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers/PlanModeRespondHandler.ts#L15)）和 `ActModeRespondHandler`（[ActModeRespondHandler.ts L10](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers/ActModeRespondHandler.ts#L10)）让模型在各自模式下发言；用户通过 `togglePlanActModeProto`（[togglePlanActModeProto.ts L13-L21](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/controller/state/togglePlanActModeProto.ts#L13-L21)）翻转模式。

## 我们的 mini 怎么体现

[`agents/s04-approval-gating`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s04-approval-gating) 把这套安全模型缩到一个能跑、离线的 Go 门控。这个对应是刻意的：

| 上游理念 | 我们的 mini |
|---------|------------|
| `shouldAutoApproveTool` 白名单 | 带每工具表的 `AutoApprovePolicy`（`approval.go`） |
| `[local, external]` 二元组 | `ToolPolicy{AutoApprove, AutoApproveExternal}` |
| `shouldAutoApproveToolWithPath` 路径检查 | `AutoApprovePolicy.shouldAutoApprove`，融合白名单 + `isLocal` |
| `askApproval` → `Task.ask` | `Approver` 接口（测试里是 `stubApprover`，`main.go` 里是 stdin Y/N） |
| 否决 → `didRejectTool` → 反馈 | `gate.Execute` 返回 `tool_result{is_error:true}` 而非中止 |
| `yoloModeToggled` / `autoApproveAllToggled` | demo 上的 `-y` 和 `-approve-all` 旗标 |

s04 唯一*没*移植的，是 Plan/Act 模式——它需要第二个模式旗标和严格模式的拒绝路径，那更偏概念而非机制。这个缺口恰恰由这份附录补上：s04 给你门控，这里给你套在它外面的模式框架。在那个目录里跑 `go run .`，看一次读取和一次工作区内写入被自动批准，而一次逃逸的写入被拒绝并作为反馈回喂——整套安全模型，浓缩在十五行输出里。

带走一个反模式（研究档案里点过名）：cline 的桌面审批在决定迟迟不来时会*无限期*阻塞——等待没有超时。一个生产级门控应当给等待设上界并安全失败（把超时当成拒绝），而不是把智能体挂死。

## 延伸阅读

- [s04 · 人类在环审批](./s04-approval-gating.md) —— 这份附录所解释的那个可运行门控。
- [s03 · 工具注册与执行](./s03-tool-registry-execution.md) —— s04 用门控包起来的那个无条件执行器。
- [附录 B · 上游映射](./appendix-b-upstream-map.md) —— 阅读真实源码的逐文件顺序。
- 上游：[`task/tools/autoApprove.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts)、[`task/ToolExecutor.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts)，以及 [`task/tools/handlers/`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers) 下的 Plan/Act handler。
