---
title: "s01 · 最小智能体循环"
chapter: 1
slug: s01-minimum-agent-loop
est_read_min: 9
---

# s01 · 最小智能体循环

> 本节讲什么：智能体循环本身——即 `请求 → 工具 → 结果 → 重复` 这个把一次性 LLM 调用变成真正能 *干活* 的东西的循环。它之所以单独成章，是因为后面每一章都是对这一个循环的精炼。

---

## Problem / 问题

一次裸的 LLM 调用对于"自主性"来说是条死路。你发一个提示词，得到一条回复，然后就没了。一旦模型说"要回答这个问题我得读 `config.go`"或者"让我跑一下测试"，一个光秃秃的 API 调用就直接停在那儿了——没有人去读文件，没有人去跑测试，也没有办法把发生了什么告诉模型。

cline 的全部工作就是填补这个鸿沟。当模型发出一个工具调用时，必须有 *某个东西* 去执行它、捕获输出、再把结果交回给模型，并询问模型下一步要做什么——如此反复，直到模型满意为止。那个"某个东西"就是智能体循环。在我们构建流式输出、工具注册表、审批门控或检查点之前，必须先构建它们全都挂靠其上的这个循环。本章构建的是真正配得上这个名字的最小版本。

## Solution / 解决方案

心智模型可以用一句话概括：**智能体不是那次 LLM 调用，而是围绕它的那个循环。**

每一轮做四件事：(1) 把整段对话外加可用的工具 schema 发给提供方；(2) 把助手的回复追加到历史里——*哪怕它是一个工具调用*，因为协议要求模型必须先看到自己之前的 `tool_use`，才能把 `tool_result` 对应上；(3) 在 `stop_reason` 上分支——如果模型请求了工具，就运行它们并把结果作为下一条 *user* 消息追加进去；如果它停下来了，就返回它的文本；(4) 循环，并设一个硬性的轮次上限，这样一个困惑的模型永远不可能无限空转。

有三个决策值得点明：

1. **统一的内部块模型。** 我们在任何地方都使用 Anthropic 原生的 `tool_use` / `tool_result` 形状；OpenAI 提供方在它的边界处做转换。循环永远不知道是哪个提供方应答的。
2. **工具结果是一个 *user* 轮次。** 这是让人意外的部分：模型的工具调用存在于一条 *assistant* 消息里，但输出要作为一条包含 `tool_result` 块的 *user* 消息回传。这种交替正是对话协议。
3. **`stop_reason` 是大脑。** 循环不靠解析文本来决定是否继续——它读 `stop_reason`。`tool_use` 表示继续；`end_turn` 表示完成。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                                                              │
│   userPrompt                                                 │
│       │                                                      │
│       ▼                                                      │
│   ┌───────────────────────┐                                 │
│   │ Provider.CreateMessage│◀──────────────┐                 │
│   └───────────┬───────────┘               │                 │
│               │ assistant turn            │ tool_result     │
│               ▼                           │ (user message)  │
│         stop_reason?                      │                 │
│          /        \                       │                 │
│   "tool_use"   "end_turn"          ┌──────┴──────┐          │
│       │             │              │  run tools  │          │
│       └────────────────────────▶  └─────────────┘          │
│                     │                                       │
│                     ▼                                       │
│                 final text  (also: MaxTurns cap aborts)     │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

承重的那 30 来行（摘自 [`agents/s01-minimum-agent-loop/loop.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s01-minimum-agent-loop/loop.go)）：

```go
// The conversation starts with the user's prompt.
messages := []Message{
	{Role: "user", Content: []ContentBlock{{Type: "text", Text: userPrompt}}},
}

for turn := 0; turn < t.MaxTurns; turn++ {
	resp, err := t.Provider.CreateMessage(ctx, CreateMessageRequest{
		Messages: messages,
		Tools:    schemas,
	})
	if err != nil {
		return "", fmt.Errorf("turn %d: %w", turn, err)
	}

	// 1. Append the assistant turn — even if it contains tool_use blocks,
	// the protocol requires it in history so the next request's tool_result
	// has a matching tool_use to point at.
	messages = append(messages, Message{Role: "assistant", Content: resp.Content})

	// 2. stop_reason is the loop's brain: keep going vs. we're done.
	switch resp.StopReason {
	case "end_turn", "stop_sequence":
		return extractText(resp.Content), nil

	case "tool_use":
		// 3. Run every requested tool, feed the results back as ONE user message.
		toolResults := t.runTools(ctx, resp.Content, toolByName, turn)
		messages = append(messages, Message{Role: "user", Content: toolResults})

	case "max_tokens":
		return "", fmt.Errorf("hit max_tokens at turn %d (response was truncated)", turn)

	default:
		return "", fmt.Errorf("unexpected stop_reason %q at turn %d", resp.StopReason, turn)
	}
}
// 4. Turn cap reached — never loop forever.
return "", fmt.Errorf("loop exceeded MaxTurns=%d without end_turn", t.MaxTurns)
```

**四个不那么显而易见的要点**：

1. **assistant 轮次在工具运行之前就被追加** ——如果你跳过它，下一个请求里的 `tool_result` 块就找不到可以引用的 `tool_use`，API 会拒绝它。
2. **工具结果作为一条 `user` 消息回传** ——而不是 assistant。每个 `tool_use` 对应一个 `tool_result` 块，各自通过 `ToolUseID` 关联。
3. **错误变成内容，而非控制流** ——一个未知工具或一个失败的工具会产生一个 *错误* `tool_result`（带 `IsError: true`），模型可以读到它并从中恢复，而不是让循环崩溃。
4. **轮次上限没有商量余地** ——没有它，一个不停调用工具的模型（或两个互相乒乓的工具）会一直循环，直到你杀掉进程。

## What Changed / 与上一节的变化

这是第一章——它确立了后面每一章都会复用的 `Task` / `Provider` / `Message` 词汇。没有上一章可供对比；取而代之，这里给出 s01 的基线，也就是整个课程后续都构建于其上的核心类型：

```go
// The wire shape (Anthropic Messages model) — our single internal vocabulary.
type ContentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"

	Text string `json:"text,omitempty"` // type == "text"

	ID    string                 `json:"id,omitempty"`    // type == "tool_use"
	Name  string                 `json:"name,omitempty"`  //  "
	Input map[string]interface{} `json:"input,omitempty"` //  "

	ToolUseID   string      `json:"tool_use_id,omitempty"` // type == "tool_result"
	ToolContent interface{} `json:"content,omitempty"`     //  "
	IsError     bool        `json:"is_error,omitempty"`    //  "
}

// The LLM call, abstracted so the loop / tests / later providers are swappable.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// One executable capability. The loop only ever sees this interface.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

// The agent loop. The smallest thing that earns the name.
type Task struct {
	Provider Provider
	Tools    []Tool
	MaxTurns int
}
```

## Try It / 动手试一试

测试完全离线运行——无需联网、无需 API key（由一个脚本化的 `fakeProvider` 驱动循环）：

```bash
cd agents/s01-minimum-agent-loop

# Run the 9 tests / see each case
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...

# Run the real agent (needs a key for your chosen provider)
export ANTHROPIC_API_KEY=sk-...
go run . -v "create hello.txt containing the text hi"

# Any OpenAI-compatible provider works too
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -v "run: echo hello from bash"
```

预期的输出形态（LLM 的输出是非确定性的，所以请对照 *形态* 而非逐字）：

```
[s01] provider=anthropic model=claude-sonnet-4-6 url=
[turn 0] assistant: I'll create that file for you.
[turn 0] -> write_file map[content:hi path:hello.txt]
[turn 0] <- wrote 2 bytes to hello.txt
[turn 1] assistant: Done — hello.txt now contains "hi".
Done — hello.txt now contains "hi".
```

如果你看到这些就说明跑对了：一行 `-> tool`、一行匹配的 `<- result`，然后是一个不带工具调用的较晚轮次，最后是 stdout 上的最终文本。出现 `loop exceeded MaxTurns` 错误意味着模型一直在调用工具却没有收尾——调高 `-max-turns` 或者把提示词写清楚一些。

## Upstream Source Reading / 上游源码阅读

cline 的循环位于 `apps/vscode/src/core/task/index.ts`（3,764 行）。循环的 *驱动器* 是 `initiateTaskLoop`；它反复调用 `recursivelyMakeClineRequests`，后者完成一个完整轮次（构建请求 → 流式 → 解析 → 运行工具 → 追加结果）并返回 `didEndLoop`。整个"我们完成了吗？"的判断就是那一个布尔值。下面是驱动器，带了少量批注：

```upstream:apps/vscode/src/core/task/index.ts#L1453-L1480
private async initiateTaskLoop(userContent: ClineContent[]): Promise<void> {
	let nextUserContent = userContent           // turn 0: the task; later: tool_results / a nudge
	let includeFileDetails = true               // workspace snapshot — only on the first request

	while (!this.taskState.abort) {             // run until cancelled or a turn ends the loop
		// ONE TURN: request → stream → parse → run tools → append tool_results.
		// Returns didEndLoop=true only when the model used no tools (i.e. stopped asking for work).
		const didEndLoop = await this.recursivelyMakeClineRequests(nextUserContent, includeFileDetails)
		includeFileDetails = false              // we only need file details the first time

		//  The way this agentic loop works is that cline will be given a task that he then calls
		//  tools to complete. unless there's an attempt_completion call, we keep responding back
		//  to him with his tool's responses until he either attempt_completion or does not use
		//  anymore tools. If he does not use anymore tools, we ask him to consider if he's
		//  completed the task and then call attempt_completion, otherwise proceed ...

		if (didEndLoop) {
			break
		}
		// Model returned only text but didn't finish: inject a "you used no tools" message and loop.
		nextUserContent = [
			{
				type: "text",
				text: formatResponse.noToolsUsed(this.useNativeToolCalls),
			},
		]
		this.taskState.consecutiveMistakeCount++
	}
}
```

**阅读笔记**：

- **两个方法 vs. 我们的一个。** cline 把 *驱动器*（`initiateTaskLoop`）和 *单个轮次*（`recursivelyMakeClineRequests`）分开，因为单个轮次是个庞大的流式例程。s01 没有流式，所以 `Task.Run` 把两者收拢进一个 for 循环——形态相同，少了些繁文缛节。
- **`didEndLoop` vs. 我们的 `stop_reason`。** cline 在轮次内部计算"是否完成"并返回一个布尔值；s01 直接读 `stop_reason == "end_turn"`。同样的判断，位置不同。
- **"未使用工具"的轻推。** cline 拒绝把"有文本但没有 `attempt_completion`"当作完成——它注入一条合成的 user 消息然后再次循环。s01 做了简化：`end_turn` 即结束循环。显式的 `attempt_completion` 工具会在后面的章节里讲。
- **轮次上限 vs. 错误次数上限。** cline 用 `maxConsecutiveMistakes` 和一个 API 请求上限来约束失控（外加用 `taskState.abort` 做取消）。s01 使用单一的硬性 `MaxTurns`——更粗糙，但它让"永不无限循环"的保证一目了然。
- **流式，有意省略。** 上游的 `recursivelyMakeClineRequests` 消费一个异步流并实时更新 UI。s01 使用带缓冲的 `CreateMessage`，这样这里你唯一需要理解的就是这个循环。s02（流式 XML 解析器）和 s05（提供方流式）会把它加回来。

**继续阅读**：从 `index.ts` → `initiateTaskLoop`（L1453）开始，沿着调用进入 `recursivelyMakeClineRequests`（L2354），然后分叉到 `api/providers/anthropic.ts` 的 `createMessage` 和 `assistant-message/parse-assistant-message.ts`。那条路径就是 s01 → s02 → s03 → s05 的真实源码地图。

---

**下一节**：s02 通过把原子化的响应替换为一个 *流* 来演进本章的循环，并讲解 cline 自定义的 XML 助手消息解析器（`parseAssistantMessageV2`），它能在消息尚未结束之前就从一个不断增长的字符串里把 `tool_use` 块提取出来。
