---
title: "s05 · 供应商流式抽象"
chapter: 5
slug: s05-provider-streaming
est_read_min: 13
---

# s05 · 供应商流式抽象

> 教什么：**供应商流式抽象**——cline 如何把约 40 个不同的 LLM API 收敛成一条由工厂选出的、归一化的异步 chunk 流，于是智能体循环只写一遍，供应商只是你启动时挑的一个字符串。

---

## Problem / 问题

s01-s04 一直依赖一个假的、缓冲式的 provider：一个请求进去，一整个 `CreateMessageResponse` 出来。为了讲清楚循环、解析器、工具注册表和审批，这种简化是对的——但它对真实模型的行为撒了谎。真实 API 是**流式**的：模型边想边吐 token，走 Server-Sent Events，cline 会实时把文本显示出来，并在消息*还没结束*之前就检测出工具调用。

由此带来两个痛点。第一，每家供应商说不同的线上方言——Anthropic 的 `content_block_delta` 和 OpenAI 的 `choices[].delta.tool_calls` 完全不同，又和 Gemini 不同。如果这种差异泄漏进循环，你会得到散落各处的 40 个特例（cline 明确警告过的反模式）。第二，你需要在运行时选供应商而不重写循环。本章同时解决两者：把每一条流归一成同一种 `StreamChunk`，再在前面放一个 `buildHandler` 工厂，让选择只是一个字符串。

## Solution / 解决方案

把 provider 建模成一个事件生成器。cline 的 `createMessage` 是一个 `async *generator`，吐出 `ApiStreamChunk` 联合体；Go 表达同一件事的惯用法是只读 channel，所以我们的 `Provider.CreateMessageStream` 返回 `<-chan StreamChunk`。

三个决策撑起本章：

1. **一个 chunk 联合体，在边界处生成。** `StreamChunk` 有五个标签——`text`、`tool_use_start`、`tool_use_delta`、`usage`、`done`。每个 provider 把自家 SSE 格式解码成恰好这几种。循环只 switch 标签，绝不 switch 供应商。
2. **流式是主路径，缓冲只是对流的一次折叠。** provider 只实现一次流式；缓冲式的 `CreateMessage`（s01 风格的便利方法）是一个共享的 `assembleResponse`，它把 channel 抽干，再把 chunk 折回成一个响应。没有任何供应商实现两遍缓冲。
3. **工厂选 provider，遇到未知就大声报错。** `buildHandler(name, cfg)` 对应 cline 的 `buildApiHandler` / `createHandlerForProvider` switch。OpenAI 兼容的别名（deepseek、moonshot、qwen、groq……）全都解析到同一个 `OpenAIProvider`，只是 base URL 不同。

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  buildHandler("anthropic"|"deepseek"|...)  ─▶  Provider          │
│                                                                  │
│  POST stream:true ─▶ SSE bytes ─▶ decode<Vendor>SSE              │
│        anthropic: message_start / content_block_delta / stop     │
│        openai:    choices[].delta.content / .tool_calls / [DONE] │
│                          │                                       │
│                          ▼                                       │
│   <-chan StreamChunk : text · tool_use_start · tool_use_delta    │
│                        · usage · done                            │
│                          │                                       │
│                          ▼                                       │
│   assembleResponse ─▶ CreateMessageResponse (text + tool_use)    │
└────────────────────────────────────────────────────────────────┘
```

核心 40 行（节选自 [`agents/s05-provider-streaming/provider.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s05-provider-streaming/provider.go)）——Anthropic 的 SSE 解码器，也就是把供应商事件映射到共享 chunk 联合体的那段：

```go
func decodeAnthropicSSE(ctx context.Context, r io.Reader, out chan<- StreamChunk) {
	activeToolID := "" // input_json_delta 帧不重复 id，记住它
	for ev := range sseEvents(r) {
		var p anthropicEvent
		if json.Unmarshal([]byte(ev.data), &p) != nil {
			continue // 跳过坏行
		}
		switch p.Type {
		case "message_start": // 一上来就带 input token 计数
			if p.Message != nil {
				u := p.Message.Usage
				send(ctx, out, StreamChunk{Type: ChunkUsage,
					Usage: &Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}})
			}
		case "content_block_start": // tool_use 块以 id + name 开场
			if p.ContentBlock != nil && p.ContentBlock.Type == "tool_use" {
				activeToolID = p.ContentBlock.ID
				send(ctx, out, StreamChunk{Type: ChunkToolUseStart,
					ToolCallID: p.ContentBlock.ID, ToolName: p.ContentBlock.Name})
			}
		case "content_block_delta":
			switch p.Delta.Type {
			case "text_delta": // 一段助手正文
				send(ctx, out, StreamChunk{Type: ChunkText, Text: p.Delta.Text})
			case "input_json_delta": // 工具输入 JSON 的一个“部分”片段
				send(ctx, out, StreamChunk{Type: ChunkToolUseDelta,
					ToolCallID: activeToolID, InputJSON: p.Delta.PartialJSON})
			}
		case "message_stop":
			send(ctx, out, StreamChunk{Type: ChunkDone})
			return
		}
	}
	send(ctx, out, StreamChunk{Type: ChunkDone}) // 即便缺 stop 也要收尾
}
```

**4 个非显然之处**：

1. **`stream:true` 就是那个开关。** 同一个 `/v1/messages` 端点，不带它返回一整个 JSON，带上它返回 SSE 事件流。provider 设置它，下游什么都不变。
2. **工具输入是部分 JSON，不是一个值。** `input_json_delta` 帧带的是 `partial_json` 字符串片段，像 `{"path":` 再 `"main.go"}`。你把每个片段拼起来，最后 `json.Unmarshal` 一次——绝不逐片段解析。
3. **delta 帧不重复工具 id。** Anthropic 在 `content_block_start` 里只发一次 id+name；我们把它存进 `activeToolID`，再盖到每个 delta 上（cline 用 `lastStartedToolCall` 做同样的事）。
4. **错误必须先于 channel 暴露。** 非 2xx 直接从 `CreateMessageStream` 返回 `error`——调用方不该靠抽干一条流才发现 401。

## What Changed / 与 s02 的变化

s02 里解析器消费的是一个**已完成的字符串**：你把累积的缓冲交给 `parseAssistantMessageV2`，它走一遍。s05 改变了字节从哪儿来——它们现在作为来自真实 provider 的实时 chunk 流到达。

```diff
 // s02：解析器吃的是提前拼好的内存字符串。
-buf += delta            // 某处已经拿到了整段（或不断增长的）文本
-blocks := parser.Blocks(buf)

 // s05：provider 流式吐出归一化 chunk；文本 delta 本身就是“喂料”。
+ch, err := provider.CreateMessageStream(ctx, req)   // <-chan StreamChunk
+for c := range ch {
+    switch c.Type {
+    case ChunkText:          // <- 这就是你会喂给 s02 解析器的东西
+    case ChunkToolUseStart:  // 原生 tool_use：id + name
+    case ChunkToolUseDelta:  // 原生 tool_use：部分输入 JSON
+    case ChunkUsage, ChunkDone:
+    }
+}
```

语义上：s02 负责解析一个*字符串*；s05 负责生产那个字符串本该从中来的*流*。两者可组合——s05 的 `ChunkText` delta 正是 s02 增量解析器想被喂的东西。还要注意：s05 暴露的是模型的**原生** `tool_use` 事件（id + name + 部分 JSON），这是 s02 那条 XML 工具调用路径的另一条路；cline 两条都支持，把它们混为一谈是经典错误。

## Try It / 动手试一试

```bash
cd agents/s05-provider-streaming

# 离线：把一段录制好的 Anthropic SSE 流喂进真实解码器。
# 无需 API key、完全确定——看着每个归一化 chunk 到达。
go run .

# 在线：从真实 provider 流式读取（需要对应档位的 key）。
export ANTHROPIC_API_KEY=sk-...
go run . -live -model claude-sonnet-4-20250514 -prompt "say hi in one line"

# 任意 OpenAI 兼容供应商都走同一条 chunk 管线。
export DEEPSEEK_API_KEY=sk-...
go run . -live -provider deepseek -model deepseek-chat -prompt "say hi"

# 测试：完全离线（假的 strings.Reader + httptest），不联网。
go test -v ./...
```

期望输出形态：

```
== offline demo: replaying a recorded Anthropic SSE stream ==
  [usage] in=42 out=1
  [text]  "Let me "
  [text]  "read that file."
  [tool]  start id=toolu_01 name=read_file
  [tool]  input += "{\"path\":"
  [tool]  input += "\"main.go\"}"
  [usage] in=0 out=17
  [done]

== assembled response ==
stop_reason: tool_use  usage: in=42 out=17
text: "Let me read that file."
tool_use: read_filemap[path:main.go]
```

每行 `[...]` 是一个按到达顺序排列的 `StreamChunk`；assembled response 是 `assembleResponse` 把它们折叠成的结果。在线运行会逐 token 不同（LLM 输出不确定），但形态一致。

## Upstream Source Reading / 上游源码阅读

cline 的等价实现在 `apps/vscode/src/core/api/providers/anthropic.ts`。`createMessage`（L64）是一个 `async *generator`；它的 `for await` 事件 switch（L183-L302）把每个解析好的 SSE 事件映射成 `api/transform/stream.ts`（L1-L70）里定义的 `ApiStreamChunk` 联合体的一个 chunk。选 provider 的工厂是 `buildApiHandler`（`api/index.ts` L478），它调用 `createHandlerForProvider` switch（L76）。主要差别：cline 用 `@anthropic-ai/sdk`，由它替你把原始 `data:` 行解析好，所以它的循环迭代的是已经带类型的事件；我们的 Go 版没有这样的 SDK，于是 `decodeAnthropicSSE` 手工解码 SSE 行协议。

```upstream:apps/vscode/src/core/api/providers/anthropic.ts#L183-L302
// Source: apps/vscode/src/core/api/providers/anthropic.ts (createMessage, simplified)
// SDK 吐出已解析的 SSE 事件；cline 把每个映射成一个 ApiStreamChunk。
const lastStartedToolCall = { id: "", name: "", arguments: "" }

for await (const chunk of stream) {
	switch (chunk?.type) {
		case "message_start": {
			// 第一个事件：input token 计数（+ prompt 缓存计数）。
			const usage = chunk.message.usage
			yield {
				type: "usage",
				inputTokens: usage.input_tokens || 0,
				outputTokens: usage.output_tokens || 0,
				cacheWriteTokens: usage.cache_creation_input_tokens || undefined,
				cacheReadTokens: usage.cache_read_input_tokens || undefined,
			}
			break
		}
		case "message_delta":
			// 运行中的 output token 计数（以及 stop_reason）。
			yield { type: "usage", inputTokens: 0, outputTokens: chunk.usage.output_tokens || 0 }
			break
		case "content_block_start":
			switch (chunk.content_block.type) {
				case "tool_use":
					// tool_use 以 id + name 开场；参数单独流式到达。
					if (chunk.content_block.id && chunk.content_block.name) {
						lastStartedToolCall.id = chunk.content_block.id
						lastStartedToolCall.name = chunk.content_block.name
					}
					break
				case "text":
					yield { type: "text", text: chunk.content_block.text }
					break
			}
			break
		case "content_block_delta":
			switch (chunk.delta.type) {
				case "text_delta":
					yield { type: "text", text: chunk.delta.text } // 一段正文切片
					break
				case "input_json_delta":
					// 部分工具输入 JSON——注意它不带 id，所以 cline 重新
					// 盖上 lastStartedToolCall。拼接，最后解析一次。
					if (lastStartedToolCall.id && chunk.delta.partial_json) {
						yield {
							type: "tool_calls",
							tool_call: { ...lastStartedToolCall, function: {
								id: lastStartedToolCall.id,
								name: lastStartedToolCall.name,
								arguments: chunk.delta.partial_json,
							} },
						}
					}
					break
			}
			break
		case "content_block_stop":
			lastStartedToolCall.id = ""   // 清空，让下一个 tool_use 干净开场
			lastStartedToolCall.name = ""
			break
	}
}
```

**对照阅读要点**：

- **async generator → channel 的映射。** 上游 `yield` 进 `ApiStream`（一个 `AsyncGenerator`）；我们 `send` 进 `<-chan StreamChunk`。同一份契约：一个被惰性生产、有序的序列，消费者去迭代。
- **我们手搓 SSE，上游不用。** cline 依赖 `@anthropic-ai/sdk` 把 `data:` 行解析成带类型的事件。Go 标准库没有 Anthropic SDK，于是 `sseEvents` + `decodeAnthropicSSE` 做这件解码（SDK 替你藏起来的那部分）。
- **reasoning 和缓存字段是故意丢掉的。** 上游还会 yield `reasoning` chunk（思考块）和缓存 token 计数；s05 只保留 `text`/`tool_use`/`usage`/`done`，专注在抽象本身。
- **`content_block_stop` 复位在途工具。** 上游清空 `lastStartedToolCall`，让同一条消息里的第二个 tool_use 干净开场；我们的 assembler 改为按 active id 归集 delta，殊途同归。
- **一个故意保留的不完美。** 如果流在没有 `message_stop` 的情况下结束（截断的 fixture），我们仍然发一个收尾的 `done`，让消费者永远可以依赖它——“正确但宽容”，而不是报错。

**想读更多**：从 `anthropic.ts` 的 `createMessage`（L64）入手，跟着 chunk 形态进 `api/transform/stream.ts` 的 `ApiStreamChunk`（L1-L70），再看同一个联合体如何被另一种线上格式在 `api/providers/openai.ts` 的 `createMessage` 里生成，最后读 `api/index.ts` 的工厂 `buildApiHandler`（L478）→ `createHandlerForProvider`（L76）。这条线就是 s05 → s06（广告工具的提示词）以及 s02（这些 delta 现在能驱动的解析器）的真实代码地图。

---

**下一节预告**：s06 构建模块化、按模型感知的**系统提示词**，告诉模型有哪些工具——也就是请求的输入侧，而 s05 刚刚学会解码的正是这个请求的流式输出。
