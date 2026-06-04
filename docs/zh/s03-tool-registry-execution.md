---
title: "s03 · 工具注册与执行"
chapter: 3
slug: s03-tool-registry-execution
est_read_min: 10
---

# s03 · 工具注册与执行

> 本节讲什么：工具注册表与分发——一个 `name → handler` 的映射，外加一个 `Execute(block)`，它查找处理器、校验参数、执行，并把结果（成功或失败）变成 `tool_result`。它单独成章，是因为"一个智能体有很多工具，模型按名字挑一个"这件事，是工具能力得以扩展的全部基础。

---

## Problem / 问题

s01 的循环里塞了一个写死的工具，s02 学会了从流式文本里解析出工具调用。但这两章都回避了一个真实智能体绕不开的事实：工具不止一个。cline 有大约 27 个工具——`read_file`、`write_to_file`、`execute_command`、`list_files`、`search_files`、MCP 调用……模型每一轮从里面**按名字**挑一个。如果分发逻辑是一长串 `if name == "read_file" { ... } else if name == "write_to_file" { ... }`，那么每加一个工具都要改循环，循环也很快会被几十个分支淹没。

更微妙的是出错路径。模型会幻觉出不存在的工具名，会漏掉必填参数，工具自身也会失败（文件不存在、命令返回非零）。这些都**不能**让智能体循环崩溃——模型必须看到那条错误，才能在下一轮纠正自己。所以我们需要的不只是一个查找表，而是一个能把任意失败都转成"模型能读懂的 `tool_result`"的分发器。本章构建的就是这个注册表加分发器。

## Solution / 解决方案

心智模型：**工具执行从"内联"变成"按名字分发到注册表"。** 注册表是一个 `map[string]ToolHandler`；执行器拿到一个解析好的 `tool_use` 块，查表、校验、执行、包装结果——仅此而已。

承重的设计是：分发器的**每一条出口都返回一个 `tool_result`**，绝不 panic。未知工具？返回错误结果。缺参数？返回错误结果。处理器报错？返回错误结果。成功？返回带输出的结果。循环因此可以无脑地把结果作为下一个 user 轮次追加进去。

有三个决策值得点明：

1. **注册表就是那个 `map`。** 这正是 cline 的 `ToolExecutorCoordinator`——一个 `Map<string, IToolHandler>`，配 `register` / `has` / `getHandler`。加一个工具 = 往 map 里塞一项，循环一行都不用动。
2. **校验先于执行。** 必填参数在 `Execute` 被调用 *之前* 检查（对应 cline 的 `ToolValidator.assertRequiredParams`）。一个缺了 `content` 的 `write_to_file` 在碰到磁盘之前就被拒掉。
3. **错误是结果，不是异常。** cline 在 `handleError` 里把抛出的异常捕获，再 `pushToolResult` 一条错误结果。我们直接返回一个 `IsError=true` 的 `ToolResult`。模型看到错误，循环继续。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                                                              │
│   parsed tool_use(block)                                     │
│        │                                                     │
│        ▼                                                     │
│   registry.Has(name)? ──no──▶ tool_result{ "unknown tool" } │
│        │ yes                                                 │
│        ▼                                                     │
│   required params? ─────no──▶ tool_result{ "missing param" }│
│        │ yes                                                 │
│        ▼                                                     │
│   handler.Execute ──────err─▶ tool_result{ err text }       │
│        │ ok                                                  │
│        ▼                                                     │
│   tool_result{ output, call_id }                            │
│                                                              │
│   （每条出口都带 call_id，循环把它作为 user 轮次喂回去）       │
└──────────────────────────────────────────────────────────────┘
```

承重的那 40 来行（摘自 [`agents/s03-tool-registry-execution/executor.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s03-tool-registry-execution/executor.go)）：

```go
// Execute dispatches one parsed tool_use block and returns a ToolResult that is
// always safe to append to the conversation.
func (e *ToolExecutor) Execute(ctx context.Context, block ToolUse) ToolResult {
	// 1. Unknown tool: do not panic, hand the model an error it can recover from.
	handler, ok := e.registry.Get(block.Name)
	if !ok {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error: unknown tool %q. Available tools: %s", block.Name, e.toolNames()),
			IsError: true,
		}
	}

	// 2. Validate required params before doing any work.
	params := flatten(block)
	if missing := assertRequiredParams(handler, params); missing != "" {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error: missing required parameter %q for tool %q.", missing, block.Name),
			IsError: true,
		}
	}

	// 3. Run the tool. A returned error is reported, not thrown.
	out, err := handler.Execute(ctx, params)
	if err != nil {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error executing %s: %v", block.Name, err),
			IsError: true,
		}
	}

	// 4. Success.
	return ToolResult{CallID: block.CallID, Output: out}
}
```

**4 个非显然之处**：

1. **`Has` 检查不可省。** 必须先 `Get`/`Has` 再分发。这一步把"模型幻觉的工具名"挡在外面，转成一条普通的错误结果——cline 的 `execute` 同样以 `if (!this.coordinator.has(block.name))` 开场。
2. **`flatten` 统一两种前端。** 原生 `tool_use` 把参数放在 `Input`（已解码的 map），s02 的 XML 解析器放在 `Params`（字符串 map）。`flatten` 把两者合并成一个 `map[string]string`，于是处理器永远不必关心是谁解析的这次调用。
3. **校验返回"哪个参数缺了"，而不是布尔值。** 错误信息里点名具体参数（`missing required parameter "content"`），这样模型能精确修正，而不是盲猜。
4. **`CallID` 贯穿每一条出口。** 成功和失败的 `ToolResult` 都带上 `block.CallID`，于是 `tool_result` 块能和原来的 `tool_use` 配对——这是 Anthropic 协议要求的，模型靠它知道"这条结果是回答哪次调用的"。

## What Changed / 与 s02 的变化

```diff
- // s01/s02: a parsed tool_use was either run by one inline tool, or just
- // surfaced as a block. There was no name-based routing.
- block := parser.Blocks()[i]          // s02 gives us parsed blocks ...
- // ... and then? s02 stops at parsing.

+ // s03: parsed blocks are now DISPATCHED through a registry.
+ reg := NewRegistry()
+ reg.Register(&ReadFileTool{Base: base})
+ reg.Register(&WriteToFileTool{Base: base})
+ reg.Register(&ListFilesTool{Base: base})
+ reg.Register(&ExecuteCommandTool{Base: base})
+ exec := NewToolExecutor(reg)
+
+ result := exec.Execute(ctx, block)   // name → handler → tool_result
```

语义上的差别：s02 在"解析出 `tool_use` 块"处就停下了——它产出块，但不知道拿块怎么办。s03 接上了下一段：这些块现在被**按名字分发**到处理器。工具执行从"内联一个"变成"注册表里多个共存"，而注册新工具不再需要碰分发逻辑——`Register` 一下即可。这也是为什么 cline 能装下约 27 个工具外加运行期的 MCP 工具：它们全都只是 map 里的条目。

## Try It / 动手试一试

```bash
cd agents/s03-tool-registry-execution

# 脚本化的分发序列：写文件、读回、列目录、跑命令，再加两个出错用例
go run .

# 同时把每个注册工具的 schema 打到 stderr
go run . -v

# 测试（完全离线，t.TempDir 沙箱）
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

期望输出形态（s03 是逐字稳定的——临时沙箱路径不会出现）：

```
[t1] write_to_file    -> ok    wrote 11 bytes to notes/hello.txt
[t2] read_file        -> ok    hi from s03
[t3] list_files       -> ok    hello.txt
[t4] execute_command  -> ok    registry-and-dispatch
[t5] fly_to_moon      -> ERROR Error: unknown tool "fly_to_moon". ...
[t6] read_file        -> ERROR Error: missing required parameter "path" ...
```

判断你跑对了：t1..t4 都是 `ok`（四个真实工具都分发并执行了）；t2 读回了 t1 写入的内容（写→读经过注册表往返）；t5 是 `unknown tool` 错误；t6 是 `missing required parameter` 错误。两个错误都返回 `IsError=true` 的 `tool_result`，循环继续，模型看得到——什么都不会 panic。

## Upstream Source Reading / 上游源码阅读

上游的等价机制分在两个文件：注册表本体在 `ToolExecutorCoordinator.ts`（一个 `Map<name, handler>` 加 register/has/getHandler/execute），而真正被 Task 循环调用的分发入口在 `ToolExecutor.ts` 的 `execute`（L312）——它先做 `has()` 检查，再用 try/catch 把任何失败转成 `tool_result`。下面是注册表的核心：

```upstream:apps/vscode/src/core/task/tools/ToolExecutorCoordinator.ts#L111-L168
// THE registry. ~27 handlers live here, keyed by tool name.
export class ToolExecutorCoordinator {
	private handlers = new Map<string, IToolHandler>()

	// register: store a handler under its own .name. registerToolHandlers()
	// (ToolExecutor.ts L201) calls this in a loop over every known tool name.
	register(handler: IToolHandler): void {
		this.handlers.set(handler.name, handler)
	}

	// has: is this tool name registered? The orchestrator calls this FIRST so an
	// unknown tool is handled gracefully instead of dispatching into nothing.
	has(toolName: string): boolean {
		return this.getHandler(toolName) !== undefined
	}

	getHandler(toolName: string): IToolHandler | undefined {
		// (real code also remaps MCP tool names + builds dynamic subagent
		//  handlers here — elided; s03 has neither.)
		return this.handlers.get(toolName)
	}

	// execute: resolve + run. THROWS on unknown tool — the caller
	// (ToolExecutor.execute) turns that into a tool_result via handleError.
	async execute(config: TaskConfig, block: ToolUse): Promise<ToolResponse> {
		const handler = this.getHandler(block.name)
		if (!handler) {
			throw new Error(`No handler registered for tool: ${block.name}`)
		}
		return handler.execute(config, block)
	}
}
```

**对照阅读要点**：

- **注册方式**：上游用 `registerByName(toolName, validator)` 经一张 `toolHandlersMap` 工厂表来 new 出每个处理器；我们直接 `reg.Register(&ReadFileTool{...})`。差别只在"是否经过一层工厂"，map 本身是一样的。
- **抛异常 vs 返回结果**：上游 `coordinator.execute` 对未知工具 *抛异常*，由外层 `ToolExecutor.execute` 的 try/catch + `handleError` 兜底成 `tool_result`。我们把这两步合并，直接返回错误结果——Go 没有异常，显式返回更顺手。
- **参数校验的位置**：上游每个处理器在自己的 `execute` 开头调 `ToolValidator.assertRequiredParams`；我们把校验上提到分发器里统一做（用处理器声明的 `RequiredParams()`）。语义一致：执行前拒绝缺参的调用。
- **我们省掉的字段**：上游 `getHandler` 还会把 MCP 工具名归一化到单个处理器、惰性构造动态 subagent 处理器；s03 既无 MCP（s09）也无 subagent，故省略。
- **故意保留的"正确但不完美"**：`execute_command` 直接用 `/bin/sh -c` 跑命令、不做任何审批。这在 s03 是对的——本章只讲注册表与分发；危险命令的人类在环门控是 s04 的主题。

**想读更多**：从 `ToolExecutor.ts` 的 `registerToolHandlers`（L201）看注册表如何被填满，再走 `executeTool`（L212）→ `execute`（L312）看每次调用的分发；跟着 `coordinator.execute`（L162）进到 `handlers/ReadFileToolHandler.ts`，最后读 `ToolValidator.ts` 的 `assertRequiredParams`（L17）。包住 `handler.execute` 的那层审批（`askApproval` → `autoApprove.ts shouldAutoApproveTool`）就是 s04。这条线就是 s03 → s04 的真实代码地图。

---

**下一节预告**：s04 在分发与 `Execute` 之间插入一道**审批门控**。同样这些工具调用，现在每一次都要先过一道策略 + 人类的关卡——读类工具自动放行，写类与 shell 命令则需确认。
