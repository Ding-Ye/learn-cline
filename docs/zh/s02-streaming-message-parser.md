---
title: "s02 · 流式消息解析器"
chapter: 2
slug: s02-streaming-message-parser
est_read_min: 11
---

# s02 · 流式消息解析器

> 教什么：cline 如何在模型的**文本流**到达过程中，把工具调用解析出来——一个增量解析器，识别 XML 风格的 `<tool><param>…</param></tool>` 块，并处理跨 chunk 边界的半截标签。它单独成章，是因为这正是 cline 能在整条消息到齐之前就**边流边动**的原因。

---

## Problem / 问题

s01 里模型说的是 Anthropic 的**原生** `tool_use` 块：API 自己就把每个工具调用返回成干净的 JSON 对象，早已和正文分好了。那是最省事的一条路，可不是每个模型都提供它。cline 支持约 40 家供应商，许多模型（凡是没有原生工具调用的）改成把工具调用**当成 XML 写在正常输出中间**——`<write_to_file><path>…</path><content>…</content></write_to_file>`。

有两点让这变得棘手。其一，响应是**流式**的：一次几个 token 地来，所以任意时刻 cline 手里只有消息的一个前缀——也许是 `<write_`，剩下的还在路上。其二，cline 想**尽早**反应：边收边显示正文，并在某个工具的标签一闭合就立刻把调用送去审批。一个只在完整消息上跑的解析器会把流式的全部意义都丢掉。我们需要一个解析器，能消费 chunk、每次重新推导块列表，并且能说出"这儿有个工具调用，但还没写完"。

## Solution / 解决方案

心智模型：**解析器是累积缓冲区的纯函数，每来一个 chunk 就重跑一遍。** `feed(chunk)` 只往缓冲区追加；`parse()` 走一遍**整个**缓冲区，返回当前块列表，若缓冲区在块中间结束就把末尾块标成 `partial`。

为什么重新解析整个缓冲区，而不是从保存的状态续跑？因为标签可能在**任意位置**被切开——`<write_` + `to_file>`。如果解析器想从半截标签处续跑，它就得跨调用记住半截标签的状态。而始终在拼接后的整串上工作，切口就直接消失了：解析器从来看不到 `<write_`，只看到最终的 `<write_to_file>`。上游 `parseAssistantMessageV2` 正是这么用的——cline 每来一个 delta 就在增长的字符串上调用它一次。

三个值得点名的决策：

1. **索引驱动，而非字符累加器。** 我们用索引 `i` 扫描，在每个位置问："在 `i` 处**结束**的子串是否匹配某个已知标签？"只有块完成时才切出内容。（V1 用逐字符累加器；V2 为了速度去掉了它——我们保留 V2 的形态。）
2. **一张"已识别工具表"决定什么算工具。** `<write_to_file>` 启动一个工具；`<thinking>` 不会——它不在表里，所以保持为**文本**。未知标签永远不是工具调用。
3. **`write_to_file` 的 content 走特例。** 文件正文本身可能含有形似 `</content>` 的文本。若普通的 param 扫描被它绊倒，我们用 `lastIndexOf("</content>")` 找回值，让真正的闭合标签胜出。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│  feed("<write_")  feed("to_file>")  feed("<path>a</path>…")   │
│        │               │                   │                  │
│        ▼               ▼                   ▼                  │
│   ┌──────────────────────────────────────────────┐          │
│   │   累积缓冲区（到目前为止的整条消息）            │          │
│   └───────────────────────┬────────────────────────┘         │
│                           │ parse()  （索引扫描，3 个状态）   │
│                           ▼                                    │
│        ┌─────────────┬─────────────┬──────────────┐          │
│        │  in-text    │  in-tool    │  in-param    │  状态    │
│        └─────────────┴─────────────┴──────────────┘          │
│                           │                                    │
│                           ▼                                    │
│   []AssistantBlock:  text │ tool_use{name,params,partial}     │
│                                  └ 标签被切断时 partial=true   │
└──────────────────────────────────────────────────────────────┘
```

扫描的核心承重部分（节选自 [`agents/s02-streaming-message-parser/parser.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s02-streaming-message-parser/parser.go)）：

```go
n := len(msg)
for i := 0; i < n; i++ {
	// --- 状态：在某个工具 PARAMETER 内部 ---
	if tool != nil && paramName != "" {
		closeTag := "</" + paramName + ">"
		if endsWith(msg, i, closeTag) {
			value := strings.TrimSpace(msg[paramValueStart : i-len(closeTag)+1])
			tool.Params[paramName] = value
			paramName = "" // 回到工具内容状态；继续往下走
		} else {
			continue // 仍在 param 值内部
		}
	}

	// --- 状态：在某个 TOOL 内部，但不在具体 param 里 ---
	if tool != nil && paramName == "" {
		started := false
		for _, name := range toolParamNames { // 开始一个新的 <param>？
			if endsWith(msg, i, "<"+name+">") {
				paramName, paramValueStart, started = name, i+1, true
				break
			}
		}
		if started {
			continue
		}
		if endsWith(msg, i, "</"+tool.ToolName+">") { // 关闭这个工具？
			tool.Partial = false // 见到闭合标签 → 完成
			blocks = append(blocks, *tool)
			tool, textStart = nil, i+1
			continue
		}
		continue // 仍在工具体内部
	}

	// --- 状态：TEXT / 寻找工具的开始 ---
	started := false
	for _, name := range recognizedTools {
		if endsWith(msg, i, "<"+name+">") {
			if inText { // 在标签开始处结束当前文本段
				if c := strings.TrimSpace(msg[textStart : i-len(name)-2+1]); c != "" {
					blocks = append(blocks, AssistantBlock{Type: "text", Text: c})
				}
				inText = false
			}
			tool = &AssistantBlock{Type: "tool_use", ToolName: name,
				Params: map[string]string{}, Partial: true} // 闭合前都是 partial
			toolContentStart, started = i+1, true
			break
		}
	}
	if started {
		continue
	}
	if !inText {
		textStart, inText = i, true // 一段新文本从这里开始
	}
}
```

**4 个非显然之处**：

1. **新工具起始就是 `partial: true`。** 只有读到它的闭合 `</tool>` 才翻成 `false`。所以若流先结束，这个块会被正确地报告为进行中——这正是 UI 能渲染"即将写文件…"的依据。
2. **`endsWith(msg, i, tag)` 是全部诀窍。** 它问的是"`tag` 是否恰好在索引 `i` 处结束？"（上游 `startsWith(tag, i-len+1)` 的 Go 写法）。于是单次正向扫描就能在标签最后一个字节到达的瞬间识别开标签和闭标签。
3. **因工具开始而结束的文本**不是* partial；末尾文本**才是**。当一个 `<tool>` 标签关闭了某段文本，那段文本是最终的。但缓冲区最末端的文本会被标成 partial——可能还有正文在流进来。（对应上游的 finalization 分支。）
4. **已识别工具表和 param 表就是语法。** 不在表里的一切都是字面文本，所以模型写 `<thinking>` 或自创 `<not_a_tool>` 时，产出的是普通文本块，绝不会冒出幽灵工具调用。

## What Changed / 与 s01 的变化

s01 的助手响应是**原子且预结构化**的——`resp.Content` 里早已是带解析好 `Input` map 的 `tool_use` 块，由供应商交付。s02 接收的是相反的输入：一个**随流增长的字符串**，块由它自己重建。

```diff
-// s01：供应商已经把工具调用拆成了结构化 JSON。
-type ContentBlock struct {
-	Type  string                 // "text" | "tool_use" | "tool_result"
-	Name  string                 // 工具名，由 API 给我们
-	Input map[string]interface{} // 工具参数，由 API 解析
-}
-// loop 直接读 resp.Content —— 无需解析。
+// s02：工具调用以 XML 文本在流中到达；我们自己解析出来。
+type AssistantBlock struct {
+	Type     string            // "text" | "tool_use"
+	ToolName string            // 从 <tool_name> … </tool_name> 解析
+	Params   map[string]string // 从 <param> … </param> 对解析
+	Partial  bool              // 闭合标签还没到时为 true
+}
+
+type StreamingParser struct{ buf strings.Builder }
+func (p *StreamingParser) feed(chunk string)  { p.buf.WriteString(chunk) }
+func (p *StreamingParser) parse() []AssistantBlock { /* 索引扫描 */ }
```

语义上的转变：s01 里"模型调用了什么工具"由 API 回答；s02 里这由**我们自己**的状态机回答——读原始文本、增量地、在字节还在到达时就回答。`Partial` 标志是全新的——在响应是原子的世界里它没有意义，而它正是流式的核心。

## Try It / 动手试一试

本章全程离线、确定性——这一章没有任何 LLM，只有一段硬编码的样例助手回合，分块喂入。

```bash
cd agents/s02-streaming-message-parser

# 以 7 字节为块喂入样例（故意把标签切到块边界两侧）
go run .

# 看每来一个块后块列表如何增长（partial 工具 → 完成）
go run . -steps

# 一次性全部喂入 —— 最终解析与分块跑逐字节相同
go run . -chunk 0

# 跑 8 个测试
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

期望输出形态（这一份**可**复现——循环里没有模型）：

```
[s02] feeding 39 chunk(s) of a streamed assistant message
=== final parse ===
  [0] text: "I'll create a small greeter for you."
  [1] tool_use: write_to_file
        path = "greet.go"
        content = "package main\n\nimport \"fmt\"\n\n// prints a greeting. ..."
  [2] text (partial): "That file is ready."
```

你跑对了的判据：块 `[1]` 是名为 `write_to_file` 的 `tool_use`，且 `path` 与 `content` **两个**参数都被抽出（尽管 7 字节分块把标签切开了），末尾块 `[2]` 是被标成 `(partial)` 的文本。用 `-steps` 跑可以看到 `[1]` 先以 partial 出现，待 `</write_to_file>` 到达后变完整。

## Upstream Source Reading / 上游源码阅读

cline 的解析器在 `apps/vscode/src/core/assistant-message/parse-assistant-message.ts`；整个函数是 `parseAssistantMessageV2`（L28-240）。它产出的联合类型——`TextStreamContent` 和 `ToolUse`——在 `assistant-message/index.ts`（L3-81），连同 `toolParamNames` 表。下面是从"文本/工具开始"状态到循环末尾的部分，它展示了文本段如何在工具开始时被关闭、新工具如何以 `partial` 开启：

```upstream:apps/vscode/src/core/assistant-message/parse-assistant-message.ts#L137-L210
// --- State: Parsing Text / Looking for Tool Start ---
if (!currentToolUse) {
	// Check if starting a new tool use
	let startedNewTool = false
	for (const [tag, toolName] of toolUseOpenTags.entries()) {
		// `startsWith(tag, i - tag.length + 1)`：tag 是否恰好在 i 处结束？
		if (currentCharIndex >= tag.length - 1 && assistantMessage.startsWith(tag, currentCharIndex - tag.length + 1)) {
			// 若有活动文本块则结束它（它在标签开始处结束）。
			if (currentTextContent) {
				currentTextContent.content = assistantMessage
					.slice(currentTextContentStart, currentCharIndex - tag.length + 1)
					.trim()
				currentTextContent.partial = false // 因工具开始而结束
				if (currentTextContent.content.length > 0) {
					contentBlocks.push(currentTextContent)
				}
				currentTextContent = undefined
			} else {
				// 上一个块与此标签之间的零散文本。
				const potentialText = assistantMessage
					.slice(currentTextContentStart, currentCharIndex - tag.length + 1)
					.trim()
				if (potentialText.length > 0) {
					contentBlocks.push({ type: "text", content: potentialText, partial: false })
				}
			}

			// 开启新工具 —— 在找到闭合标签前都是 PARTIAL。
			currentToolUse = {
				type: "tool_use",
				name: toolName as ClineDefaultTool,
				params: {},
				partial: true,
				call_id: nanoid(8),
				isNativeToolCall: false,
			}
			currentToolUseStart = currentCharIndex + 1 // 内容从标签之后开始
			startedNewTool = true
			break
		}
	}
	if (startedNewTool) {
		continue
	}

	// 不是工具标签 → 是文本。若尚未在文本块中则开启一个。
	if (!currentTextContent) {
		currentTextContentStart = currentCharIndex
		currentTextContent = { type: "text", content: "", partial: true }
	}
	// 内容稍后抽取（工具开始时或 finalization 时）。
}
```

**对照阅读要点**：

- **`startsWith(tag, i - tag.length + 1)` ⇄ 我们的 `endsWith(msg, i, tag)`。** 想法完全一致："这个标签是否在索引 `i` 处结束？"TypeScript 在偏移处检查前缀；Go 切片再比较。两者都让单次正向扫描在标签最后一个字节到达时立刻命中——流式的关键。
- **工具开启时的 `partial: true`。** 上游设法与我们相同，且只有闭合标签分支（L127，`currentToolUse.partial = false`）会翻转它。底部的 finalization 块（L223-226）把任何仍打开的工具压入但**不**清 partial——被截断的流就是这样报告进行中的工具的。
- **`call_id: nanoid(8)` 我们丢掉了。** 上游给每个工具调用打 id（后续用于匹配流式 UI 更新和原生工具结果）。s02 没有 UI、这里也没有原生路径，故省略；s03/s05 在身份真正有用时再引入。
- **`write_to_file` / `content` 特例**（上游 L107-125，我们的 `lastIndexOf("</content>")`）。文件正文可能含有形似闭合标签的文本；上游在工具的内层切片上用 `indexOf`/`lastIndexOf` 找回 `content`。我们原样照搬——这是朴素标签扫描唯一会出错的地方。
- **一个我们故意保留的"正确但不完美"的设计。** 每次 `parse()` 都重解析整个缓冲区，每次 O(n)，整条流下来就是 O(n²)。上游也接受这点（助手消息很小）；可续跑的解析器更快，但要携带半截标签状态，那会毁掉让 partial 处理显然正确的那份简洁。

**想读更多**：从 `parse-assistant-message.ts` 的 `parseAssistantMessageV2`（L28）入手，读 param-close 与 tool-close 状态（L51-135），再读 finalization 块（L212-240）；对照 `assistant-message/index.ts` 里 `ToolUse` / `toolParamNames` 的定义（L13-81）。这条线就是 s02 → s03（执行解析出的 `tool_use`）→ s05（绕过本解析器的原生工具调用流）的真实代码地图。

---

**下一节预告**：s03 把本解析器产出的 `tool_use` 块**执行**起来——一个按名分发的工具注册表（`ToolExecutor`）：查找处理器、校验参数、执行，并把输出包成 `tool_result` 块喂回 s01 的循环。
