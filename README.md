# learn-cline

> 用 Go 从零渐进重写 [cline](https://github.com/cline/cline) 的自主编码智能体内核——每一章配一段上游 TypeScript 源码精读。

[English](./README.en.md) · 简体中文

## 这是什么

cline 是一个自主编码智能体：你给它一个任务，它读代码、改文件、跑命令，每一步都经过你的审批。它的核心其实很简单——**一个围绕 LLM 的循环**。

这个仓库不教你「用」 cline，而是教你「它的智能体内核怎么从零长出来」。每一章用 Go 写一份能独立运行的精简实现，只讲清楚 cline 的一个机制；章末的「上游源码阅读」把你的 mini 版和上游真实的 TypeScript 生产代码（`apps/vscode/src/core/`）一一对照，让你顺着指针从几百行读进数千行的工程实现。

每一章都是一个**自包含的 Go module**（`learn-cline/sNN`），不跨章引用，可以单独 `go run`。

## 课程目录

| # | 章节 | 机制 | 状态 |
|---|------|------|------|
| s01 | [最小智能体循环](docs/zh/s01-minimum-agent-loop.md) | 请求 → 响应 → 工具 → 结果 → 循环 | ✅ |
| s02 | [流式消息解析器](docs/zh/s02-streaming-message-parser.md) | 从增长的字符串中增量解析 XML 工具调用 | ✅ |
| s03 | [工具注册与执行](docs/zh/s03-tool-registry-execution.md) | 按名分发的工具注册表 + `tool_result` 封装 | ✅ |
| s04 | [人类在环审批](docs/zh/s04-approval-gating.md) | 审批门控 + 自动批准白名单 | ✅ |
| s05 | [供应商流式抽象](docs/zh/s05-provider-streaming.md) | 解析 SSE 流 + 供应商工厂 | ✅ |
| s06 | [模块化系统提示词](docs/zh/s06-system-prompt.md) | 变体驱动的提示词构建 + 工具规格注入 | ✅ |
| s07 | [文件编辑与差异应用](docs/zh/s07-file-edit-diff.md) | SEARCH/REPLACE 差异块的流式应用 | ✅ |
| s08 | [上下文窗口管理](docs/zh/s08-context-window-management.md) | 预算内的安全截断 | ✅ |
| s09 | [MCP 外部工具集成](docs/zh/s09-mcp-integration.md) | stdio JSON-RPC 客户端 + 远程工具适配 | ✅ |
| s10 | [影子 Git 检查点](docs/zh/s10-checkpoints-shadow-git.md) | 影子 git 的每步快照与恢复 | ✅ |
| s_full | 集成全貌 | 把以上机制接成一个完整的 cline 智能体 | ✅ |
| A | 附录 A · 审批安全模型 | 人类在环 + Plan/Act 模式 | ✅ |
| B | 附录 B · 上游映射 | 每一章到上游源码的对照表 | ✅ |

## 快速开始

跑第一章的最小智能体循环：

```bash
cd agents/s01-minimum-agent-loop
go run . "build a TODO CLI"
```

s01 默认用一个脚本化的 fake provider，离线即可运行。接入真实 LLM 时，设置 `ANTHROPIC_API_KEY` 环境变量，并用 `-provider` 选择供应商（供应商抽象在 s05 引入）。

## Web 文档阅读器

本仓库自带一个 Next.js 的双语文档阅读器：

```bash
cd web
npm install
npm run dev
```

打开 http://localhost:3000 即可浏览各章节文档与上游源码导读。

## 致谢

- 上游 [cline/cline](https://github.com/cline/cline)，Apache-2.0 许可证——本仓库是其智能体内核的教学性 Go 重写。
- 教学方式受 [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/analysis_claude_code) 启发。

## 许可证

[MIT](./LICENSE)

## 多模型支持

所有调用 LLM 的章节（s01、s05）都通过 Provider 抽象支持多家后端：Anthropic 原生 + 任意 OpenAI 兼容端点（DeepSeek / Qwen / Moonshot / Groq / OpenRouter / 本地 vLLM）。详见 [多模型接入指南](docs/zh/multi-model.md)。
