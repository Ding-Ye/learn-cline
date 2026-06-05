---
title: "集成全貌 · 把十章拼成一个 cline 智能体"
chapter: full
slug: s_full-integration
est_read_min: 16
---

# 集成全貌 · 把十章拼成一个 cline 智能体

> 这一章不引入任何新机制。它把前十章（s01–s10）各自打磨的零件按 cline `Task` 的真实顺序串成一条流水线，回答一个问题：**这些孤立的小模块合在一起，到底是怎么变成一个能自主改代码的智能体的？** 读完你应当能闭着眼睛画出"供应商流 → 循环 → 解析 → 工具 → 审批"这条主干，并知道每一步对应仓库里哪个 Go 函数。

---

## 架构总览

cline 的核心其实只有一句话：**智能体不是那一次 LLM 调用，而是围着它转的那个循环**。一个模型说"我想读这个文件"之后就停住了——除非有东西真的去读、把结果喂回去、再问它一次。那个"带着结果再问一次"就是循环（s01），而其余九章都是挂在这条循环上的能力。

下面是这套迷你栈的全貌。实线是每一轮（turn）都会走的主干；虚线是按需触发的旁路能力。

```
            ┌──────────────────────────────────────────────────────────┐
            │                      Task 循环  (s01)                      │
            │              loop.go: Task.Run —— 一轮的发动机             │
            └───┬───────────────┬───────────────┬──────────────────┬────┘
                │ 1.请求         │ 3.解析         │ 5.派发           │ 8.下一轮
                ▼               ▼               ▼                  │
      ┌───────────────┐ ┌──────────────┐ ┌──────────────┐         │
      │ 供应商流 (s05) │ │ 流式解析 (s02)│ │ 工具注册表(s03)│         │
      │ Anthropic SSE │ │  增量出块     │ │  按名派发     │         │
      └───────┬───────┘ └──────────────┘ └──────┬───────┘         │
              │ 2.数据块流                        │ 4.审批门         │
              │                                  ▼                  │
        ┌─────┴──────┐                   ┌──────────────┐          │
        │ 系统提示(s06)│··· 把工具写进 ···▶│ 审批门控(s04) │          │
        │  模块化构建  │     提示词         │ 自动批准/人审 │          │
        └────────────┘                   └──────┬───────┘          │
                                          6.批准 │ 执行             │
                ┌─────────────────────────┬──────┴──────┐          │
                ▼                         ▼             ▼          │
        ┌──────────────┐         ┌──────────────┐ ┌──────────┐    │
        │ 文件编辑(s07) │         │ MCP 工具 (s09)│ │ 其它工具  │    │
        │ SEARCH/REPLACE│         │ 外部进程载入  │ │read/exec │    │
        └──────────────┘         └──────────────┘ └──────────┘    │
                │ 7.每轮提交                                        │
                ▼                                                  │
        ┌──────────────┐         ┌──────────────┐                 │
        │ 检查点 (s10)  │         │ 上下文管理(s08)│◀────────────────┘
        │  影子 git     │         │  超预算就截断  │  9.下一轮前裁剪历史
        └──────────────┘         └──────────────┘
```

一句话定位：**s01 是骨架，s05/s02 把模型的话变成结构化的块，s03/s04 决定哪个工具能跑、要不要人点头，s06 告诉模型有哪些工具，s07/s09 是真正干活的手，s08/s10 在背后管住"历史别撑爆"和"任何一步都能回退"。** 把它们按上图接好，得到的就不是一堆零件，而是 cline 这个智能体本身。

---

## 执行轨迹

下面用一个真实任务走一遍整条循环：用户说 **"给 `greet.go` 加一个函数并验证它能编译"**。每一步都标注了它在仓库里对应的 Go 文件与函数（均已确认存在）。这条轨迹是 cline `apps/vscode/src/core/task/index.ts` 中 `recursivelyMakeClineRequests` 主循环的迷你映射，也对应研究笔记 A3 的 16 步端到端追踪。

1. **建提示词。** 先按模型族选一个变体（Claude-4 走 next-gen 原生工具，其它走 generic XML），把 s03 注册表里的工具规格渲染进 `TOOL USE` 段。 → `agents/s06-system-prompt/variants.go`（`SelectVariant`）+ `prompt.go`（`PromptBuilder.Build`）。

2. **开循环。** `Task.Run` 把用户那句话塞成第一条 `user` 消息，进入 `for turn < MaxTurns` 的发动机；`MaxTurns` 是防止模型抽风时无限打转的硬闸。 → `agents/s01-minimum-agent-loop/loop.go`（`Task.Run`）。

3. **发请求、流式拿回。** 循环把 `系统提示 + 历史 + 工具规格` 交给供应商，向 `POST /v1/messages` 带 `stream:true`，开始读 Server-Sent Events。 → `agents/s05-provider-streaming/provider.go`（`AnthropicProvider.CreateMessageStream`）。

4. **解 SSE、归一化数据块。** `decodeAnthropicSSE` 把 `content_block_start / content_block_delta / message_stop` 这些厂商事件翻译成统一的 `StreamChunk`（text / tool_use_start / tool_use_delta / usage / done）——循环只认这一种块，永远不碰厂商格式。 → `agents/s05-provider-streaming/provider.go`（`decodeAnthropicSSE`）。

5. **增量解析助手消息。** 文本增量喂进流式解析器；它把累积缓冲区重新解析成 `text` 与 `tool_use` 块，缓冲区在标签中途断掉时把尾块标 `partial`。模型这一轮决定调用 `replace_in_file`。 → `agents/s02-streaming-message-parser/parser.go`（`StreamingParser.feed` / `parse`）。

6. **派发到注册表。** 解析出的 `tool_use` 块交给执行器，按名在注册表里查处理器；查不到不会 panic，而是回一条 `IsError` 的 `tool_result` 让模型自己纠错。 → `agents/s03-tool-registry-execution/executor.go`（`ToolExecutor.Execute`）+ `tools.go`（`Registry.Get`）。

7. **过审批门（自动批准快路）。** `replace_in_file` 是写操作，先问策略：路径在工作区内且工具在白名单里就自动放行，否则升级给人。`read_file` 这类只读工具会在这一步被自动批准、连问都不问。 → `agents/s04-approval-gating/approval.go`（`AutoApprovePolicy.shouldAutoApprove`）。

8. **过审批门（人审升级）。** 策略不放行时升级到人；这里的两级决策（先策略后人审）就是门控的核心，被拒绝不会中止任务，而是回一条反馈 `tool_result`。 → `agents/s04-approval-gating/gate.go`（`ApprovalGate.decide` / `ApprovalGate.Execute`）。

9. **应用文件编辑。** 批准后，`replace_in_file` 把 `------- SEARCH / ======= / +++++++ REPLACE` 块定位到原文里的对应片段并替换，再写回磁盘（沙箱内）。 → `agents/s07-file-edit-diff/diff.go`（`constructNewFileContent`）+ `edit.go`（`applyDiffToFile`）。

10. **生成 tool_result。** 门控把工具输出（或错误文本）包成带 `CallID` 的 `tool_result` 块——这个 id 让模型把结果对回它刚才发出的 `tool_use`。 → `agents/s04-approval-gating/gate.go`（`ApprovalGate.Execute`）。

11. **每轮存检查点。** 这一轮改完文件后，影子 git 做一次 `git add -A` + `git commit --allow-empty`，返回的哈希作为本轮 `Checkpoint` 记下，整条历史对用户的真实 `.git` 完全不可见。 → `agents/s10-checkpoints-shadow-git/checkpoint.go`（`ShadowGit.Commit`）。

12. **回填历史、继续循环。** 循环把这一轮的 `assistant` 消息和 `tool_result`（作为下一条 `user` 消息）追加进历史，回到第 3 步发下一次请求。 → `agents/s01-minimum-agent-loop/loop.go`（`Task.Run` 的 `runTools`）。

13. **下一轮前裁剪上下文。** 发下一个请求前先估算 token；若超预算就计算一段要删的中间消息区间（永远保留第一对 user/assistant 与最近若干轮），并剥掉新首条消息上游离的 `tool_result`。 → `agents/s08-context-window-management/context.go`（`estimateTokens` / `ContextManager.overBudget` / `truncate`）。

14. **第二轮：模型要验证。** 重复第 3–6 步，这次模型调用 `execute_command` 跑 `go build ./...`。这是一条独立的工具，从注册表里同样按名取出。 → `agents/s03-tool-registry-execution/tools.go`（`Registry.Get`，`ExecuteCommandTool`）。

15. **完成检测。** 模型看到编译通过，这一轮不再发任何 `tool_use`，`stop_reason` 为 `end_turn`；循环据此判定任务完成并返回最终文本。 → `agents/s01-minimum-agent-loop/loop.go`（`Task.Run` 的 `end_turn` 分支）。

16.（可选）**回退。** 若用户事后想撤销，按某个 `Checkpoint` 的哈希做 `git reset --hard`，工作区文件即刻回到那一轮的状态，且时间线可继续往后接。 → `agents/s10-checkpoints-shadow-git/checkpoint.go`（`ShadowGit.Restore`）。

> 上面第 9 步中的 MCP 工具（s09）在这条轨迹里没被触发，但它是同一个派发口的旁路：一个外部 MCP 服务器的工具经 `DiscoverTools` 适配成普通 `Tool` 后注册进同一个表，之后第 6 步对它和 `read_file` 一视同仁。 → `agents/s09-mcp-integration/mcp.go`（`McpClient.ListTools` / `DiscoverTools` / `CallTool`）。

---

## 跨章交互图

下面这张时序图按一轮（one turn）的真实顺序画出对象间的消息往来。重点是区分**稳定契约**（跨章不变的数据形状，换实现也不破）与**动态派发**（运行时才定的分支）。

```
用户          Task循环      供应商         解析器        执行器        审批门        工具/编辑     影子git
(s01)         (s05)        (s02)         (s03)         (s04)        (s07/s09)    (s10)
 │             │            │             │             │             │            │
 │ 任务文本    │            │             │             │             │            │
 ├────────────▶│            │             │             │             │            │
 │             │ CreateMessageStream(req) │             │             │            │
 │             ├───────────▶│             │             │             │            │
 │             │  «StreamChunk» 流 ◀───── │             │             │            │   ← 稳定契约：StreamChunk
 │             │◀───────────┤             │             │             │            │     (text/tool_use/usage/done)
 │             │ feed(text 增量)          │             │             │            │
 │             ├─────────────────────────▶│             │             │            │
 │             │  «AssistantBlock» 块 ◀── │             │             │            │   ← 稳定契约：AssistantBlock
 │             │◀─────────────────────────┤             │             │            │     (type/name/params/partial)
 │             │ Execute(toolUse)         │             │             │            │
 │             ├──────────────────────────────────────▶│             │            │
 │             │              registry.Get(name) ★ 动态派发：按名查处理器          │
 │             │                                        │ decide(name,params)      │
 │             │                                        ├────────────▶│            │
 │             │                          ★ 动态派发：策略→人审 判定批准/拒绝       │
 │             │                                        │◀────────────┤            │
 │             │                                        │ tool.Execute(input)      │
 │             │                                        ├──────────────────────────▶│  (s07 改文件 / s09 调 MCP)
 │             │                                        │◀──────────────────────────┤
 │             │   «ToolResult» (CallID, output) ◀──── │             │            │   ← 稳定契约：ToolResult
 │             │◀──────────────────────────────────────┤             │             │     (call_id 对回 tool_use)
 │             │ Commit("turn N")                       │             │             │
 │             ├───────────────────────────────────────────────────────────────────▶│
 │             │   hash ◀────────────────────────────────────────────────────────── │
 │             │ overBudget? → truncate(history)  (s08，下一轮前)                     │
 │             │ 追加 assistant + tool_result 到历史，loop ↺                          │
```

图里要记住三件事：

- **三条稳定契约**贯穿全程，是各章解耦的关键。`Provider` 接口（`CreateMessageStream` 返回 `StreamChunk` 通道）让循环不关心是哪个厂商在答；`AssistantBlock` 是解析器的唯一产物（`type/name/params/partial`），让 s02 与 s03 之间只靠这一个形状对话；`ToolResult` 携带 `CallID`，把工具结果稳稳对回模型发出的 `tool_use`。换掉任意一章的内部实现，只要这三个形状不变，循环就不用改一行。
- **两处动态派发**是运行时才决定的分支，正是"智能体会变"的地方。其一是 `registry.Get(name)`——同一个 `Execute` 入口，按模型这一轮报的名字取出不同处理器（`read_file` 还是 `replace_in_file` 还是某个 MCP 工具）。其二是审批判定——`decide` 先问策略再问人，结果（自动批准 / 人审通过 / 拒绝）每一次都可能不同，且拒绝会折成反馈喂回模型而非中止。
- **旁路与主干同口**：s07 的文件编辑和 s09 的 MCP 工具并不在循环里各开一条路，它们都从 `tool.Execute` 这一个口子出去——这正是 s03 注册表的价值：新增能力只是往表里多塞一个 `Tool`，主干一行不动。

---

## 故意省略

这个集成版只保留"能跑通一轮真实任务"所需的主干。cline 真实产品里有大量同样重要、但会把教学代码淹没的特性，本课程一律不实现。下表列出它们以及略去的原因，方便你日后读上游源码时心里有数。

| 省略的 cline 特性 | 上游位置（参考） | 为什么这里不做 |
|---|---|---|
| Plan / Act 双模式 UI | `PlanModeRespondHandler.ts` / `ActModeRespondHandler.ts` | 这是"何时该审批"的产品分层（先探讨后执行），属于交互策略；核心循环只需"每个副作用工具都过门控"即可，模式切换不改循环骨架。详见附录 A。 |
| Webview / gRPC 前端 | `apps/vscode/src/core/controller/` + `webview-ui/` | 前端只是循环的一个客户端，可被 CLI 或别的 UI 替换；教学版用 stdin/stdout 与注入决策代替，避免引入整套 IPC。 |
| 浏览器工具 | `services/browser/BrowserSession.ts` | 需要驱动无头浏览器，依赖重且与"智能体循环"主题正交；它只是注册表里的又一个工具，原理已被 s03/s09 覆盖。 |
| 终端集成（长驻进程/输出回流） | `integrations/terminal/CommandExecutor.ts` | s03 的 `execute_command` 只做一次性命令；真实的 PTY 管理、流式 stdout、中断信号是一整套终端工程，超出主干所需。 |
| Focus chain / 任务待办 | `apps/vscode/src/core/task/` 相关状态机 | 这是把长任务拆成可追踪子目标的产品能力，属于循环之上的编排层；不影响"请求→工具→结果"这条主干。 |
| 遥测 / 可观测性 | `posthog-node`、`@opentelemetry/*` | 埋点与链路追踪是运维关注点，对理解机制无帮助，反而会让每个函数多出无关分支。 |
| 完整的 40+ 供应商清单 | `apps/vscode/src/core/api/providers/`（40+ 文件） | s05 只做 Anthropic SSE + 一个 OpenAI 兼容路径，已足够演示"归一化到一种数据块 + 工厂选择"；其余厂商是同一模式的重复。 |
| `.clinerules` 项目规则 | 工作区 `.clinerules` / `.clinerules/` | 这是把团队规范声明式地喂进系统提示的能力；s06 已展示提示词如何模块化拼装，规则文件只是又一个待注入的来源。 |
| 上下文压缩（摘要式 condense） | `ContextManager.ts` 的摘要路径 | s08 只实现"截断"（删旧消息）这一种回收策略；"调一次模型把旧历史总结成一段"是更贵也更复杂的有损压缩，留作延伸。 |
| MCP 的 SSE/HTTP 传输与 OAuth | `services/mcp/McpHub.ts`（~1500 行） | s09 只走 stdio JSON-RPC，足以展示"外部进程的工具如何变成本地 `Tool`"；鉴权与多传输是生产细节，不是机制本身。 |

略去这些不是因为它们不重要，而是因为**机制的骨架在没有它们时反而最清楚**。把上表当成一张"接下来读什么"的地图：每一行都是一个你已经理解了主干、可以独立深入的方向。

---

## 延伸阅读

- **附录 A：审批安全模型** —— 把 s04 的门控放回 Plan/Act 的语境，讲清楚"为什么每个破坏性工具默认都要人点头"，以及拒绝如何作为反馈回流而非中止任务。
- **附录 B：上游映射** —— 一张把每一章对回 cline 真实源码文件与符号的速查表；读完玩具实现后照着它去读 `apps/vscode/src/core/` 的原版。
- **上游主循环** —— `apps/vscode/src/core/task/index.ts` 的 `initiateTaskLoop`（L1453）与 `recursivelyMakeClineRequests`（L2354）：本章这条 16 步轨迹的完整、生产级原型（含取消、锁、呈现调度等本课略去的部分）。
- **研究笔记 A3** —— `.learn/research-notes.md` 第 8 节的 16 步端到端追踪，从 CLI 入参一直走到会话落盘，是本章"执行轨迹"在真实 CLI 里的对应版本。
- **逐章回看** —— 若某一步看不懂，回到对应章节文档（`docs/zh/s01`…`s10`）：每章都用"问题→方案→原理→上游对照"的六段式把那个机制讲透。
