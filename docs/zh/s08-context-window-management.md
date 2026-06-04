---
title: "s08 · 上下文窗口管理"
chapter: 8
slug: s08-context-window-management
est_read_min: 12
---

# s08 · 上下文窗口管理

> 教什么：**上下文窗口管理**——cline 如何在对话不断变长时，把它压在模型的 token 预算之内：截断掉**中间**的旧消息，同时始终保留第一条任务消息和最近几轮，且不破坏 API 要求的 `tool_use`/`tool_result` 配对。

---

## Problem

到目前为止，每一章都让对话无限增长。智能体循环（s01）一轮轮地追加：user 轮、assistant 轮、再一条带 `tool_result` 的 user 轮。工具注册（s03）和文件编辑差异（s07）让每一轮更**臃肿**：单次 `read_file` 或一个 `final_file_content` 块就可能是几千个字符。读改十几个文件之后，这段历史会变得巨大。

但每个模型的**上下文窗口**是固定的（Claude 4 约 200K token，小模型/本地模型则小得多），超出窗口的请求会被 API 直接拒绝。于是那个本来在愉快改文件的智能体突然失败——不是因为任务难，而是因为它**记得太多**。两种朴素做法都是错的：丢**最旧**的消息会删掉模型赖以保持方向的任务定义；丢**最新**的消息会删掉它正在进行的工作。而粗暴地切片还会让某个 `tool_result` 变成孤儿（它对应的 `tool_use` 被删了），这也会被 API 拒绝。本章把这三件事一并解决。

## Solution

把历史当成一个有预算的列表，从**中间**裁剪。心智模型是一个三明治：保留第一对 user/assistant（任务，模型的北极星），保留最近几轮（它正在干活的地方），丢掉已经完成使命的陈旧中段。

三个关键决策点：

1. **先估算，再触发。** 用 char/4 启发式（`estimateTokens`）序列化 system + messages + tools 来近似请求成本。当估算值超过 `Budget` 时，执行截断。cline 触发的依据是*上一个*请求的真实用量；我们估算的是*即将发出*的这个请求。无论哪种，都是一次比较。
2. **保留第一对、丢掉偶数个中段、以 assistant 结尾。** `nextTruncationRange` 永远从索引 2 开始删除（索引 0、1 是任务，绝不动），删除的数量按偶数计算（整对），如果最后被删的不是 assistant 消息就回退一个——这样保留下来的尾部会从一条 *user* 消息接上，维持严格的 user-assistant-user-assistant 顺序。
3. **在接缝处剥离孤儿 tool_result。** 拼回 `[第一对] + [尾部]` 后，新的尾部首条可能是一条 user 轮，其 `tool_result` 指向已被删除的 `tool_use`。`applyTruncation` 会把这些块过滤掉，让请求保持合法。

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  estimateTokens(System + Messages + Tools)  >  Budget ?         │
│        │ 否                              │ 是                    │
│        ▼                                 ▼                       │
│  原样返回                   nextTruncationRange(messages)         │
│                              start=2（保留第一对）               │
│                              删偶数个中段，以 assistant 结尾      │
│                                          │                       │
│                                          ▼                       │
│  applyTruncation:  [0,1]  +  messages[end+1:]                    │
│                              └─ 剥离孤儿 tool_results ───────┐   │
│                                                             ▼   │
│   [ 任务对 ] ............（被丢弃）............ [ 最近的尾部 ]   │
└────────────────────────────────────────────────────────────────┘
```

核心 30 行节选（取自 [`agents/s08-context-window-management/context.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s08-context-window-management/context.go)）——决定丢什么的范围运算：

```go
func (cm *ContextManager) nextTruncationRange(messages []Message) (start, end int, ok bool) {
	const rangeStart = 2 // 索引 0 和 1 是第一对 user/assistant —— 保留

	n := len(messages)
	if n <= rangeStart+cm.KeepRecent {
		return 0, 0, false // 消息太少，没有可安全丢弃的
	}

	startOfRest := rangeStart
	lastKeepable := n - cm.KeepRecent // 最近尾部要保留的第一个索引
	droppable := lastKeepable - startOfRest
	if droppable <= 0 {
		return 0, 0, false
	}

	messagesToRemove := droppable
	if messagesToRemove%2 != 0 {
		messagesToRemove-- // 力求整对（对应上游的偶数）
	}
	if messagesToRemove <= 0 {
		return 0, 0, false
	}

	end = startOfRest + messagesToRemove - 1 // 闭区间末端

	// 确保最后被删的是 assistant 消息，使保留尾部从一条 user 消息接上
	//（对应上游的 rangeEndIndex -= 1 保护）。
	if end >= 0 && end < n && messages[end].Role != "assistant" {
		end--
	}
	if end < startOfRest {
		return 0, 0, false
	}
	return startOfRest, end, true
}
```

**四个非显然之处**：

1. **第一对不可侵犯。** `rangeStart` 硬编码为 2。任务定义（以及 assistant 的首次确认）锚定了整个会话；删掉它们会让模型乱走，哪怕*近期*上下文看起来正常。
2. **偶数个，但保护逻辑会故意打破奇偶。** 删除数按偶数计算（整对），但 assistant 结尾的保护可能把它减成奇数。这是有意的：真正承重的不变量是*边界角色*（保留尾部从 user 开始），而不是删了多少个。
3. **孤儿 tool_result 会让请求崩溃。** 一个 `tool_result` 块必须紧跟其 `tool_use`。如果截断删掉了发起调用的 assistant 轮、却留下带结果的 user 轮，API 会报错。`applyTruncation` 把接缝消息里这些残留剥掉。
4. **一次不一定够。** 每次请求只删一段有界的中段。如果结果仍超预算（最近的几轮很臃肿），cline 不会循环——它只是在*下一个*请求时再截断一次。`-budget 1500` 这个 demo 演示的正是这种情况。

## What Changed (vs. s07)

到 s07 为止，历史是只增不减的：每一个工具结果——包括 s07 那种完整的 `final_file_content` 块——都被加进去且永不移除。s08 在请求发出*之前*插入了一道预算闸门。

```diff
 // s01-s07：历史永远增长；每一轮（以及每个臃肿的文件块）都留着。
-req := CreateMessageRequest{Messages: append(history, newTurn...)}
-resp, _ := provider.CreateMessage(ctx, req)   // 迟早：413 / 上下文溢出

 // s08：发送前先做预算检查并裁剪。
+req := CreateMessageRequest{Messages: append(history, newTurn...)}
+cm := NewContextManager(budget, keepRecent)
+req.Messages = cm.truncate(req)               // 超预算就丢掉陈旧中段
+resp, _ := provider.CreateMessage(ctx, req)   // 请求现在塞得进窗口了
```

语义上：s07 让每一轮可能变得*巨大*（整文件差异结果），这正是把窗口撑满的元凶——所以 s08 是它天然的对冲。`Message` 类型没有变化；新增的是对 `[]Message` 列表的一遍处理：裁掉中段，同时保护两端和工具调用配对。注意一处有意的缺口：cline 在截断前会先尝试*压缩*历史（折叠重复的文件读取）；s08 只教截断这一步。

## Try It

```bash
cd agents/s08-context-window-management

# 默认：25 条消息的历史超出 3000 token 预算 → 触发截断。
go run .

# 巨大的预算 → 无可丢弃；历史原样返回。
go run . -budget 100000

# 紧张的预算 → 一次不够（最近几轮很臃肿）；截断后仍超预算。
go run . -budget 1500

# 多保留几轮最近的，或造一段更长的对话来截断。
go run . -keep 8 -turns 30

# 测试：完全离线，无网络、无 LLM。
go test -v ./...
```

期望输出形态：

```
[s08] budget=3000 keepRecent=4 turns=12
  before: messages=25  est_tokens=13443  over_budget=true
  roles:  u a u a u a u a u a u a u a u a u a u a u a u a u
  drop range: [2..19] (18 messages, even=true, last-removed role="assistant")
  after:  messages=7  est_tokens=2328  over_budget=false
  roles:  u a u a u a u
  truncated=true
  invariants: first_task_msg_kept=true recent_tail_kept=true no_orphan_tool_result=true
```

本 demo 是确定性的（无 LLM），所以与 `testdata/expected.txt` 逐字一致。`roles` 这两行让不变量一目了然：`after` 时间线仍以保留的 `u a` 对开头，并以最近的尾部结束。

## Upstream Source Reading

cline 的等价实现在 `apps/vscode/src/core/context/context-management/ContextManager.ts`。`getNewContextMessagesAndMetadata`（L227）从保存的 `ClineApiReqInfo` 读取上一个请求的真实 token 用量，与 `getContextWindowInfo(api).maxAllowedSize` 比较，选出 `keep = "half"` 或 `"quarter"`（quarter 情况用于切换到窗口更小的模型，此时砍一半还不够）。随后它调用 `getNextTruncationRange`（L299，见下）拿到索引范围，再用 `getAndAlterTruncatedMessages`（L344）构建裁剪后的列表。与我们移植版的主要差别：cline 触发依据是*实测*用量，并把每条消息的改动持久化到磁盘以支持检查点；我们估算即将发出的请求，且全部留在内存里。

```upstream:apps/vscode/src/core/context/context-management/ContextManager.ts#L299-L339
// Source: ContextManager.ts getNextTruncationRange (simplified)
// 返回要删除的消息索引闭区间 [start, end]。
public getNextTruncationRange(
	apiMessages: Anthropic.Messages.MessageParam[],
	currentDeletedRange: [number, number] | undefined,
	keep: "none" | "lastTwo" | "half" | "quarter",
): [number, number] {
	// 永远保留第一对 user-assistant；从索引 2 开始截断。
	const rangeStartIndex = 2
	const startOfRest = currentDeletedRange ? currentDeletedRange[1] + 1 : 2

	let messagesToRemove: number
	if (keep === "half") {
		// 剩余对的一半：先 /4 再 *2，保证结果为偶数（整对）。
		messagesToRemove = Math.floor((apiMessages.length - startOfRest) / 4) * 2
	} else {
		// "quarter"：删 3/4——切换到更小窗口的模型时需要
		//（例如 claude 200k -> deepseek 64k，砍一半不够）。
		messagesToRemove = Math.floor(((apiMessages.length - startOfRest) * 3) / 4 / 2) * 2
	}

	let rangeEndIndex = startOfRest + messagesToRemove - 1

	// 最后被删的必须是 assistant 消息，使保留对之后的消息是 user 消息——
	// 维持 user-assistant-user-assistant 结构。
	if (apiMessages[rangeEndIndex] && apiMessages[rangeEndIndex].role !== "assistant") {
		rangeEndIndex -= 1 // 可能把删除数变成奇数——这是可以接受的
	}

	return [rangeStartIndex, rangeEndIndex] // 要删除的闭区间
}
```

**对照阅读要点**：

- **`keep` 是策略，不是数量。** 上游通常选 `"half"`，当砍一半仍超出（可能新换的更小）窗口时选 `"quarter"`。我们的移植只有一种策略——把陈旧中段一直丢到 `KeepRecent`——是同一思路，只是用一个旋钮代替枚举。
- **先偶数计算，再有打破奇偶的保护。** 上游把 `messagesToRemove` 算成偶数（按对计数），随后 `role !== "assistant"` 检查可能减一。我们两者都复现，且测试断言的是*边界角色*而非奇偶——正因为那道保护可能把它变成奇数。
- **孤儿剥离在兄弟方法里。** 上游在 `applyContextHistoryUpdates`（L482-505）移除孤儿 `tool_result`，并在 `ensureToolResultsFollowToolUse`（L375）进一步修复配对。我们把关键的剥离折进了 `applyTruncation`。
- **我们估算；上游实测。** cline 从最后一条 `api_req_started` 消息读取 `tokensIn + tokensOut + cacheWrites + cacheReads`。没有实时用量时，我们的 `estimateTokens` 序列化请求再除以 4——本章对这个替身是明示的。
- **一处有意省略。** 截断前上游会先跑 `attemptFileReadOptimization`（L626）：把重复的文件读取折叠成 "[duplicate]" 提示，能省下 >=30% 时就*跳过*截断。我们只教截断；那项优化是上面一层。

**想读更多**：从 `ContextManager.ts` 的 `getNewContextMessagesAndMetadata`（L227）入手，跟着 `getNextTruncationRange`（L299）进 `getAndAlterTruncatedMessages`（L344），再到 `applyContextHistoryUpdates`（L482）和 `ensureToolResultsFollowToolUse`（L375）。这条线就是 s08 的真实代码地图——从"上一个请求太大了"到"这是一份合法的、裁剪后的消息列表"的完整路径。

---

**下一节预告**：s09 在运行时扩展工具注册表，从外部 **MCP** 服务器加载工具——这是*增加*能力，而非*削减*上下文，正好是 s08 的互补轴。
