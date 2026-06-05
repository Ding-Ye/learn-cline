---
title: "附录 B · 上游映射"
appendix: B
slug: appendix-b-upstream-map
est_read_min: 10
---

# 附录 B · 上游映射

> 一份在你建完玩具之后阅读 *真实* cline 源码的参考。下文每一个路径和行号都钉死在 [cline](https://github.com/cline/cline) @ `a209825116dca469c80af4be53989638dd329f38`。全程教学目标：`apps/vscode/src/core/`（最初的 VS Code 扩展智能体），而非更新的 `sdk/packages/` 无头 SDK。

## 阅读顺序

cline 的 `task/index.ts` 约 3800 行；别从那儿开始。按你构建它们的顺序读各个部件——每一章的 mini 给你一个上游文件的词汇表，于是读到最后，那个大循环读起来就是"我已经认识的那些部件，接在一起"。

1. [`task/index.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/index.ts) —— `initiateTaskLoop`（L1453）→ `recursivelyMakeClineRequests`（L2354）。循环的形状。**（s01）** 只略读。
2. [`assistant-message/parse-assistant-message.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/parse-assistant-message.ts) —— `parseAssistantMessageV2`（L28）。一个增长的字符串如何变成工具调用。**（s02）**
3. [`task/ToolExecutor.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/ToolExecutor.ts) —— `registerToolHandlers`（L201）、`executeTool`（L212）、coordinator 分发（L575）。注册表。**（s03）**
4. [`task/tools/autoApprove.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts) —— `shouldAutoApproveTool`（L42）。安全门。**（s04、附录 A）**
5. [`api/providers/anthropic.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/api/providers/anthropic.ts) —— `createMessage`（L64）+ [`api/index.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/api/index.ts) `buildApiHandler`（L478）。工厂背后的真实流式。**（s05）**
6. [`prompts/system-prompt/index.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/prompts/system-prompt/index.ts) —— `getSystemPrompt`（L16）。模块化、变体驱动的提示词。**（s06）**
7. [`assistant-message/diff.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/assistant-message/diff.ts) —— `constructNewFileContent`（L245）、`constructNewFileContentV2`（L823）。SEARCH/REPLACE 应用。**（s07）**
8. [`context/context-management/ContextManager.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/context/context-management/ContextManager.ts) —— `getNextTruncationRange`（L299）。预算内截断。**（s08）**
9. [`services/mcp/McpHub.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/services/mcp/McpHub.ts) —— `connectToServer`（L286）、`callTool`（L1233）。动态外部工具。**（s09）**
10. [`integrations/checkpoints/CheckpointTracker.ts`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/integrations/checkpoints/CheckpointTracker.ts) —— `create`（L127）、`commit`（L212）、`resetHead`（L336）。影子 git 撤销。**（s10）**

## 文件-章节映射

每一行都已核实在钉死的 sha 上存在。"行号"指向锚定该章的那个符号。

| 上游文件 | 行号 | 它做什么 | 我们的章节 |
|---------|------|---------|-----------|
| `apps/vscode/src/core/task/index.ts` | L1453-1466 | `initiateTaskLoop` —— 播下递归请求循环的种子 | s01 |
| `apps/vscode/src/core/task/index.ts` | L2354 | `recursivelyMakeClineRequests` —— 一轮：请求 → 流式 → 工具 → 递归 | s01 |
| `apps/vscode/src/core/task/index.ts` | L661 | `Task.ask` —— 暂停并请求人工输入（审批传输通道） | s04、附 A |
| `apps/vscode/src/core/task/index.ts` | L826 | `Task.say` —— 把状态/输出流式发到 UI | s04、附 A |
| `apps/vscode/src/core/assistant-message/parse-assistant-message.ts` | L28-240 | `parseAssistantMessageV2` —— 增量 XML 工具调用解析器 | s02 |
| `apps/vscode/src/core/assistant-message/index.ts` | L3-81 | `AssistantMessageContent` / `ToolUse` 联合 + `toolParamNames` | s02 |
| `apps/vscode/src/core/task/ToolExecutor.ts` | L201-205 | `registerToolHandlers` —— 把每个 handler 注册进 coordinator | s03 |
| `apps/vscode/src/core/task/ToolExecutor.ts` | L212 | `executeTool` —— Task 循环按工具块调用的公开入口 | s03 |
| `apps/vscode/src/core/task/ToolExecutor.ts` | L575 | `coordinator.execute` —— 按名分发到解析出的 handler | s03 |
| `apps/vscode/src/core/task/tools/autoApprove.ts` | L42-117 | `shouldAutoApproveTool` —— 每工具自动批准白名单 | s04、附 A |
| `apps/vscode/src/core/task/tools/autoApprove.ts` | L122-167 | `shouldAutoApproveToolWithPath` —— 白名单 + 工作区路径检查 | s04、附 A |
| `apps/vscode/src/core/task/tools/types/UIHelpers.ts` | L56-59 | `askApproval` —— 把 webview 回答收敛成是/否 | s04、附 A |
| `apps/vscode/src/core/task/tools/handlers/PlanModeRespondHandler.ts` | L15-58 | Plan 模式回复工具（`PLAN_MODE`） | 附 A |
| `apps/vscode/src/core/task/tools/handlers/ActModeRespondHandler.ts` | L10-30 | Act 模式回复工具（`ACT_MODE`） | 附 A |
| `apps/vscode/src/core/api/providers/anthropic.ts` | L64-300 | `createMessage` —— Anthropic SSE 流 → `ApiStream` 块 | s05 |
| `apps/vscode/src/core/api/index.ts` | L76 | `createHandlerForProvider` —— 供应商 switch | s05 |
| `apps/vscode/src/core/api/index.ts` | L478 | `buildApiHandler` —— 供应商工厂 | s05 |
| `apps/vscode/src/core/prompts/system-prompt/index.ts` | L16-21 | `getSystemPrompt` —— 模块化提示词构建器的入口 | s06 |
| `apps/vscode/src/core/assistant-message/diff.ts` | L1-3, L245, L823 | SEARCH/REPLACE 标记 + `constructNewFileContent` / `...V2` | s07 |
| `apps/vscode/src/core/context/context-management/ContextManager.ts` | L227 | `getNewContextMessagesAndMetadata` —— 组装 + 可能截断 | s08 |
| `apps/vscode/src/core/context/context-management/ContextManager.ts` | L299-348 | `getNextTruncationRange` / `getAndAlterTruncatedMessages` | s08 |
| `apps/vscode/src/services/mcp/McpHub.ts` | L286, L677 | `connectToServer` / `fetchToolsList` | s09 |
| `apps/vscode/src/services/mcp/McpHub.ts` | L1233 | `callTool` —— 调用一个远程 MCP 工具 | s09 |
| `apps/vscode/src/integrations/checkpoints/CheckpointTracker.ts` | L127, L212, L336 | `create` / `commit` / `resetHead` | s10 |
| `apps/vscode/src/integrations/checkpoints/CheckpointGitOperations.ts` | L59 | `initShadowGit` —— 在工作区树上建独立 git 目录 | s10 |

## 符号对照表

我们的 Go 名字对它们的上游 TypeScript 等价物。（cline 的循环是一个大 `Task` 类；我们的词汇表把它拆成小类型——所以有几行把一个 Go 类型映射到 `Task`/`ToolExecutor` 的一个*方法或字段*。）

| 我们的类型/函数 | 上游等价物 | 文件:行号 |
|----------------|-----------|----------|
| `Task.Run` | `Task.recursivelyMakeClineRequests` / `initiateTaskLoop` | `task/index.ts:2354,1453` |
| `Message` / `ContentBlock` | Anthropic Messages 线格式（`ClineStorageMessage`） | `api/providers/anthropic.ts:64` |
| `AssistantMessageParser` | `parseAssistantMessageV2` | `assistant-message/parse-assistant-message.ts:28` |
| `AssistantMessageBlock` | `AssistantMessageContent` / `ToolUse` | `assistant-message/index.ts:3,63` |
| `ToolExecutor` | `ToolExecutor`（类）+ `registerToolHandlers` | `task/ToolExecutor.ts:43,201` |
| `ToolExecutor.Execute` | `ToolExecutor.executeTool` → `coordinator.execute` | `task/ToolExecutor.ts:212,575` |
| `Tool`（接口） | `IToolHandler`（handlers/*） | `task/tools/handlers/ReadFileToolHandler.ts` |
| `Approver` | `askApproval` → `Task.ask` | `task/tools/types/UIHelpers.ts:56` |
| `AutoApprovePolicy` | `AutoApprove`（类） | `task/tools/autoApprove.ts:8` |
| `AutoApprovePolicy.shouldAutoApprove` | `shouldAutoApproveTool` | `task/tools/autoApprove.ts:42` |
| `ToolPolicy{AutoApprove, AutoApproveExternal}` | `[local, external]` 二元组 | `task/tools/autoApprove.ts:96,163` |
| `Decision`（枚举） | `didRejectTool` 标志 + `yesButtonClicked` | `task/ToolExecutor.ts`、`UIHelpers.ts:58` |
| `Provider.Stream` / `StreamEvent` | `ApiHandler.createMessage` / `ApiStreamChunk` | `api/providers/anthropic.ts:64` |
| `buildProvider`（工厂） | `buildApiHandler` / `createHandlerForProvider` | `api/index.ts:478,76` |
| `PromptBuilder` / `getSystemPrompt` | `PromptBuilder` / `getSystemPrompt` | `prompts/system-prompt/index.ts:16` |
| `constructNewFileContent` | `constructNewFileContent` / `...V2` | `assistant-message/diff.ts:245,823` |
| `ContextManager.getNextTruncationRange` | `ContextManager.getNextTruncationRange` | `context/context-management/ContextManager.ts:299` |
| `McpHub`（连接/列工具/调工具） | `McpHub.connectToServer` / `fetchToolsList` / `callTool` | `services/mcp/McpHub.ts:286,677,1233` |
| `Checkpoint` + 影子 git | `CheckpointTracker.create` / `commit` / `resetHead` | `integrations/checkpoints/CheckpointTracker.ts:127,212,336` |
| （影子 git 初始化） | `GitOperations.initShadowGit` | `integrations/checkpoints/CheckpointGitOperations.ts:59` |

## 建议练手

1. **找一个我们没移植的工具。** 打开 [`task/tools/handlers/`](https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/handlers)，挑一个（比如 `SearchFilesToolHandler` 或 `BrowserToolHandler`）。追踪它的名字在哪里注册（`registerToolHandlers`，ToolExecutor.ts L201）、它的自动批准策略在哪里（autoApprove.ts L42）。把等价的工具加进你的 s03/s04 注册表。
2. **端到端跟一次拒绝。** 从 `WriteToFileToolHandler`（L79 处的 `ask`）出发，进 `Task.ask`（task/index.ts L661），找到 `didRejectTool`（ToolExecutor.ts L325）在哪里把否决变成 `formatResponse` 形状的反馈而非中止。和你 s04 里的 `gate.Execute` 对比。
3. **diff v1 vs v2。** 读 `constructNewFileContent`（diff.ts L245）和 `constructNewFileContentV2`（L823），列出 v2 加了什么（提示：流式 `isFinal`、模糊匹配）。s07 移植了哪些行为、概括了哪些？
4. **追踪 native vs XML 工具调用的分叉。** s02 解析 XML；native 工具调用走另一条路。找到 `createMessage`（anthropic.ts L64）在哪里发出 tool-call 增量，和 `parseAssistantMessageV2` 对比。一个 Claude-4 模型会在哪里与通用模型分道？
5. **理清截断算法。** 读 `getNextTruncationRange`（ContextManager.ts L299），确认你 s08 测试断言的"保留首个用户/助手对、移除偶数条、升级到 3/4"规则。

## 注意事项

- **行号钉死在 `a209825116dca469c80af4be53989638dd329f38`。** cline 迭代很快（研究档案记录了 30 天内多次发布）。如果你 `git pull` 上游，符号*名字*仍能搜到，但上面每张表里的*行号*都会漂移——按符号搜，别按行号。
- **一个 monorepo 里两个智能体。** 这里的一切都针对 `apps/vscode/src/core/`（正统的 VS Code 智能体）。`sdk/packages/` 树是一个独立的、更新的无头 SDK，有它自己的 `agent-runtime.ts`；它的机制*名字*重叠但行号不重叠。别串线。
- **`task/index.ts` 约 3800 行。** s01 的锚点（L1453、L2354）只是*循环的形状*；周围的取消、focus-chain、加锁、呈现调度代码都被刻意排除在课程之外。
- **`diff.ts` 同时带 v1 和 v2。** s07 教 v2（L823）语义；v1（L245）作为分发器的回退保留。只引其一不引其二会让对照该文件的读者困惑。
- **MCP 和 checkpoints 体量很大。** `McpHub.ts`（约 1500 行）和 checkpoints 集成包含玩具跳过的 OAuth、传输层和多根处理。所引符号是承重核心，不是整个文件。
