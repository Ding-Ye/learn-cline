---
title: "s07 · 文件编辑与差异应用"
chapter: 7
slug: s07-file-edit-diff
est_read_min: 13
---

# s07 · 文件编辑与差异应用

> 教什么：cline `replace_in_file` 背后的**差异应用器**——模型如何通过发出 SEARCH/REPLACE 块来编辑文件，应用器如何定位每个 SEARCH（先精确、再逐行去空白）并拼接 REPLACE，以及为什么整套逻辑被写成可以随着 diff 流式到达而逐步应用。

---

## Problem / 问题

到 s06 为止，智能体已经能给出丰富的、模型感知的系统提示词，并通过带审批门的注册表来分发工具。其中一个工具负责写文件——但我们目前唯一的写文件工具是 `write_to_file`，它从一个内容字符串**整文件**重写。对于新建文件这没问题，cline 一开始也正是这么编辑任何东西的。可它也是给一个 500 行文件改一行的糟糕方式：模型得把全部 500 行完美地重新吐一遍，烧掉大量输出 token，而任何漂移都会损坏文件。

cline 的答案是 `replace_in_file`：模型只发出它想改的那些切片，作为 **SEARCH/REPLACE** 块。SEARCH 文本说"找到这块精确区域"，REPLACE 文本说"换成这个"。本章要解决的痛点有两个。第一是*匹配*：模型发出的 SEARCH 文本极少与文件逐字节一致（缩进会漂、空白会差），所以朴素的 `strings.Index` 会不停失败——我们需要优雅的退化策略。第二是*流式*：diff 是从 provider（s05）一个 token 一个 token 到达的，cline 会实时展示编辑过程，所以应用器必须能从一个不完整的 diff 产出正确的部分结果，并且无论一次喂进还是分成一百块喂进，最终结果都相同。

## Solution / 解决方案

把编辑建模成一个对文本的函数，而不是对文件的原地修改：`constructNewFileContent(diff, original, isFinal) -> newContent`。它逐行走过 diff，维护一个极小的状态机（idle → 处于 SEARCH → 处于 REPLACE）和一个指向原文的游标 `lastProcessedIndex`。

三个关键决策撑起本章：

1. **分层匹配，顺序固定。** 对每个 SEARCH 块，先尝试**精确**子串匹配（从 `lastProcessedIndex` 向前找，这样绝不会匹配到已应用编辑的后面），再退化到**逐行去空白**匹配——它比较各行时忽略每行的首尾空白，但拼接的仍是*逐字节精确*的原文跨度。cline 还有第三层针对 3+ 行块的锚点匹配；我们只做概述，不移植。
2. **结果是"拼出来"的，不是"打补丁"。** 我们不是在偏移处修改缓冲区，而是*追加*：先写 `original[last:matchStart]`，再流式写入 REPLACE 各行，然后推进 `last = matchEnd`；在最后一块时追加尾部的 `original[last:]`。正因为只追加，一个不完整的 diff 自然产出最终结果的一个正确前缀——这就是流式之所以"免费"的原因。
3. **空 SEARCH 即新建或整文件替换。** 空 SEARCH 配空原文是*新建*文件（纯插入）；配非空原文则替换整个文件。同一个引擎因此既服务 `write_to_file` 式的新建，也服务外科手术式的编辑。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────────┐
│  diff 行               original = "a\nb\nc\n"        result        │
│  ------- SEARCH                                       ""            │
│   b              ─┐                                                 │
│  =======         │  locate(b) → [2,4)  精确|逐行去空白             │
│                  │  result += orig[last:2] = "a\n"   → "a\n"       │
│   B              │  流式写入 REPLACE 行                → "a\nB\n"   │
│  +++++++ REPLACE ─┘  last = 4                                       │
│                                                                    │
│  （每个块重复一次，按文件顺序）                                     │
│  isFinal → result += orig[last:] = "c\n"            → "a\nB\nc\n"  │
└──────────────────────────────────────────────────────────────────┘
```

核心 40 行（节选自 [`agents/s07-file-edit-diff/diff.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s07-file-edit-diff/diff.go)）——标记循环与拼接：

```go
for _, line := range lines {
	switch {
	case matchSearchStart(line):
		inSearch = true
		inReplace = false
		currentSearch.Reset()
		continue

	case matchSearchEnd(line):
		// SEARCH 结束；在原文里定位它，并输出到匹配点为止的所有内容，
		// 让 result 在这次编辑之前都与原文保持一致。
		inSearch = false
		inReplace = true

		search := currentSearch.String()
		if search == "" {
			if len(originalContent) == 0 { // 新文件：从顶部插入
				searchMatchIndex, searchEndIndex = 0, 0
			} else { // 替换整个已有文件
				searchMatchIndex, searchEndIndex = 0, len(originalContent)
			}
		} else {
			start, end, ok := locate(originalContent, search, lastProcessedIndex)
			if !ok {
				return "", fmt.Errorf("the SEARCH block:\n%s\n...does not match anything in the file",
					strings.TrimRight(search, "\n"))
			}
			searchMatchIndex, searchEndIndex = start, end
		}
		result.WriteString(originalContent[lastProcessedIndex:searchMatchIndex])
		continue

	case matchReplaceEnd(line):
		lastProcessedIndex = searchEndIndex // 越过已匹配的跨度
		inSearch, inReplace = false, false
		currentSearch.Reset()
		searchMatchIndex, searchEndIndex = -1, -1
		continue
	}

	switch { // 普通内容行——保留尾部 "\n" 以便精确匹配
	case inSearch:
		currentSearch.WriteString(line + "\n")
	case inReplace:
		if searchMatchIndex != -1 { // 一旦知道落点，立刻流式写入替换内容
			result.WriteString(line + "\n")
		}
	}
}
```

**4 个非显然之处**：

1. **为什么只追加让流式变免费** —— 因为结果只会被延长（`orig[last:match]`，然后替换行，最后 `orig[last:]`），喂入 diff 的一个*前缀*就会产出最终结果的一个*前缀*。这里没有单独的"流式代码路径"；`isFinal` 只控制要不要追加尾部原文。
2. **只向前匹配是特性而非局限** —— `locate` 从 `lastProcessedIndex` 开始找，所以后面的块不会意外匹配到前面块已经消费掉的文本。代价是块必须按文件顺序出现；cline 接受同样的约束（并额外加了一个我们略过的乱序补救）。
3. **逐行去空白匹配拼接的是原文，不是去空白后的文本** —— 退化匹配用 `strings.TrimSpace` 比较每行以容忍缩进漂移，但返回的 `[start,end)` 跨度覆盖的是*真实*的原文字符，所以文件自己的空白被逐字节保留。
4. **尾部换行被刻意保留** —— 我们把 split 掉的 `"\n"` 重新加回。cline 自己的注释（diff.ts L427）解释了原因：去掉它会破坏对原文的整行匹配，而且我们无法判断模型的最后一行是"部分行"还是只是以换行结尾。

## What Changed / 与 s06 的变化

s06 产出的系统提示词*宣告*了 `write_to_file`（也会宣告 `replace_in_file`）。s07 把 `replace_in_file` 变成真的。文件工具的接口从整文件写入，变成一个 diff 引擎外加一个薄薄的工具：

```diff
-// s06：唯一的文件编辑工具整文件覆写。
-type WriteToFileTool struct{}
-func (WriteToFileTool) Apply(root string, in map[string]any) (ContentBlock, error) {
-    return write(root, in["path"], in["content"])     // 粗暴：把所有内容重发一遍
-}
+// s07：一个流式 diff 应用器支撑起外科手术式的编辑工具。
+func constructNewFileContent(diff, original string, isFinal bool) (string, error) { ... }
+
+type ReplaceInFileTool struct{}
+func (ReplaceInFileTool) Apply(root string, in map[string]any) (ContentBlock, error) {
+    updated, err := applyDiffToFile(root, in["path"], in["diff"]) // 定位 + 拼接
+    if err != nil { return errorResult(err.Error()), nil }       // 把错误回喂给模型
+    return ContentBlock{Type: "tool_result", ToolContent: "edited " + ...}, nil
+}
```

语义上，编辑不再是"模型重新生成文件"，而变成"模型描述一个增量，由我们来应用"。匹配失败不再让循环崩溃——它返回一个*错误* `tool_result`，与 cline 的做法完全一致，于是模型能读到"你的 SEARCH 没匹配上"并重试。

## Try It / 动手试一试

```bash
cd agents/s07-file-edit-diff

# 把一个两块的 diff 应用到种子文件上，并观察流式过程（离线，无需 key）。
go run .

# 测试——全离线，使用 t.TempDir。
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

期望输出形态（本 demo 是确定性的——没有 LLM——所以可逐字节复现；完整记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s07-file-edit-diff/testdata/expected.txt)）：

```
== edited main.go ==
package main

import "fmt"

func greeting() string { return "hello, cline" }

func main() {
	fmt.Println(greeting())
}

== streamed application (incremental result length) ==
  after  1/12 diff lines: result is 0 bytes
  after  3/12 diff lines: result is 28 bytes
  ...
  final result == one-shot result: true
```

如果流式结果与一次性结果不一致，或某个块匹配到了错误的跨度，那就是"只追加"这个不变式被破坏了——这是第一个要检查的地方。

## Upstream Source Reading / 上游源码阅读

cline 的应用器位于 `apps/vscode/src/core/assistant-message/diff.ts`。分发器 `constructNewFileContent`（L245）挑选一个版本；`constructNewFileContentV1`（L266）就是我们 Go 移植所对照的流式循环。下面的节选是它的核心：逐行的标记循环加上分层匹配与拼接。

```upstream:apps/vscode/src/core/assistant-message/diff.ts#L305-L393
for (const line of lines) {
	if (isSearchBlockStart(line)) {
		inSearch = true
		currentSearchContent = ""
		currentReplaceContent = ""
		continue
	}

	if (isSearchBlockEnd(line)) {
		inSearch = false
		inReplace = true

		if (!currentSearchContent) {
			// Empty search block
			if (originalContent.length === 0) {
				// New file scenario: nothing to match, just start inserting
				searchMatchIndex = 0
				searchEndIndex = 0
			} else {
				// V1 在此报错；V2（以及我们的 Go 移植）把空 SEARCH 当作整文件替换。
				throw new Error("Empty SEARCH block detected with non-empty file. ...")
			}
		} else {
			// 1) 精确匹配，从上一次应用的编辑处向前。
			const exactIndex = originalContent.indexOf(currentSearchContent, lastProcessedIndex)
			if (exactIndex !== -1) {
				searchMatchIndex = exactIndex
				searchEndIndex = exactIndex + currentSearchContent.length
			} else {
				// 2) 逐行去空白退化匹配（忽略每行空白）。
				const lineMatch = lineTrimmedFallbackMatch(originalContent, currentSearchContent, lastProcessedIndex)
				if (lineMatch) {
					;[searchMatchIndex, searchEndIndex] = lineMatch
				} else {
					// 3) 针对 3+ 行块的锚点退化匹配（我们的移植里只做概述）。
					const blockMatch = blockAnchorFallbackMatch(originalContent, currentSearchContent, lastProcessedIndex)
					if (blockMatch) {
						;[searchMatchIndex, searchEndIndex] = blockMatch
					} else {
						throw new Error(`The SEARCH block:\n${currentSearchContent.trimEnd()}\n...does not match anything in the file.`)
					}
				}
			}
		}

		// 对按序的替换，输出到匹配点为止的所有内容
		result += originalContent.slice(lastProcessedIndex, searchMatchIndex)
		continue
	}
	// isReplaceBlockEnd → 推进 lastProcessedIndex；内容行累积进 search
	// 或流式写入 result。（延续至 L438）
}
```

**对照阅读要点**：

- **匹配顺序完全一致**：精确 → 逐行去空白 → 锚点。我们忠实移植前两层；锚点这一层（diff.ts L132-L185）只做概述，因为一旦有了逐行去空白它很少触发，而移植它会让匹配器代码翻倍却没多少教学价值。
- **空 SEARCH 故意不同**：V1 对"空 SEARCH 配非空文件"*抛错*（一个针对畸形标记的防护）；V2 的 `beforeReplace`（L662-L672）把它当作整文件替换。我们的 Go 移植采用更友好的 V2 规则，这也正是"用 replace 来实现 write_to_file"所想要的。
- **乱序替换**：V1 带了一条 `pendingOutOfOrderReplacement` 路径（L370-L385），并在最后对替换重新排序。我们把它砍掉、要求文件顺序，让游标逻辑保持线性、让流式不变式显而易见。
- **尾部换行的微妙处**：cline 保留 `+ "\n"` 并解释（L427-L428）说不去掉它就无法保证整行退化匹配——我们把代码和理由一起照搬。
- **一个我们刻意保留的"正确但不完美"的选择**：在最后一块时，若仍有块没闭合，我们*报错*，而不是尽力输出一个半成品文件。cline 的 V2 也这么做（`getResult` L591-L593："File processing incomplete"）；大声失败胜过把半应用的编辑写进磁盘。

**想读更多**：从 `diff.ts` 的 `constructNewFileContent`（L245）入手，进入 `constructNewFileContentV1`（L266）看上面的循环，再读 `lineTrimmedFallbackMatch`（L51）以及 V2 的类 `NewFileContentConstructor`（L499）。这条线就是 s07 → s08 的真实代码地图（diff 喂进了 s08 必须控制在预算内的上下文）。

---

**下一节预告**：s08 把图景从*编辑*推进到*记忆*——一旦智能体读过文件、应用过 diff，对话就会超出模型的上下文窗口，s08 会在 token 预算下截断旧的轮次，同时不让 tool_result 变成孤儿。
