---
title: "s06 · 模块化系统提示词"
chapter: 6
slug: s06-system-prompt
est_read_min: 13
---

# s06 · 模块化系统提示词

> 教什么：**模块化系统提示词** —— cline 如何把模型的指令从一堆有序、可复用的 Section 组件里拼装出来，由模型家族对应的 Variant 选择，并把工具规格和规则注入进去，而不是写死一大段字符串。

---

## Problem / 问题

到 s05 为止，智能体已经能从真实 provider 流式读取、解析 delta（s02）、把解析出的工具调用通过注册表分发（s03）、并对每次调用做审批门控（s04）。但这一切底下有一个无声的假设：模型已经"知道"有哪些工具、自己该怎么行动。它并不知道。模型对自己工作的全部认知，就是**系统提示词**告诉它的那点东西。如果提示词从没描述过 `read_file`，模型就永远不会发出一个 `read_file` 调用给 s03 去分发。

最朴素的做法是写死一段提示词字符串，把工具贴进去。可一旦你有不止一个模型，这就崩了：Claude 4 要原生工具调用，小的本地模型要紧凑提示词，只支持 XML 的模型要 `<tool_name>` 块。而且你也没法手工让每个 variant 里的工具描述保持同步。cline 的答案是**构建**提示词 —— 把它从有序的 section 拼出来、按模型家族选定集合与顺序、并把工具注册表自动渲染进提示词。本章把这套构建器做成一个迷你版。

## Solution / 解决方案

把系统提示词当成一次**拼装**，而不是一个常量。`PromptBuilder` 注册一组 **Section** 组件（角色、工具用法格式、能力、MCP 服务器列表、规则、系统信息）。一个由模型家族选定的 **Variant** 声明：包含哪些 section、以什么顺序、以及任何按 section 的覆盖。`Build(variant, vars)` 渲染每个 section、替换 `{{PLACEHOLDER}}` 槽位、再拼接起来。

三个关键决策点：

1. **Section 是有序的、可组合的、可丢弃的。** variant 的 `ComponentOrder` 是布局的唯一真相来源。一个渲染为空的 section（没有连接 MCP 服务器时的 MCP section）会被丢掉，连分隔符一起 —— 于是同一套组件能根据上下文自适应，调用处不需要任何条件判断。
2. **工具注册表喂给提示词。** 每个注册的 `ToolSchema`（s03）被渲染成一个 `## <name>` 块，带 Description、required/optional 的 Parameters 列表、以及一个 Usage 的 XML 骨架。模型*能*调用的工具清单恰好就是注册表，绝不是手工维护的副本。
3. **variant 选择"方言"。** `generic` 把工具渲染成提示词里的 XML `<tool_name>` 块；`next-gen` 省掉 XML 块、把工具作为原生规格挂在请求上、并覆盖出一段更紧凑的 RULES。`SelectVariant(modelID)` 是首个匹配命中，`generic` 作为保底兜底 —— 这正是 cline 的 matcher 循环。

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  SelectVariant("claude-opus-4.8") ─▶ Variant{order, native, …}   │
│                                              │                   │
│  PromptBuilder.Build(variant, vars):         ▼                   │
│    for id in ComponentOrder:                                     │
│       Section.Render ─▶ map[id]=body   role · tool_use(+specs) · │
│                                        mcp? · capabilities ·     │
│    resolve {{CWD}} {{OS}} {{SECTION}}  rules · system_info       │
│       unknown {{KEY}} ─▶ 原样保留              │                 │
│    postProcess: 丢空 ====、trim               ▼                 │
│                                                                  │
│  ─▶ 一条 system-prompt 字符串 + 原生 specs（仅 next-gen）        │
└────────────────────────────────────────────────────────────────┘
```

核心 40 行（节选自 [`agents/s06-system-prompt/prompt.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s06-system-prompt/prompt.go)）—— `Build`，那条贯穿全章的三步流水线：

```go
func (b *PromptBuilder) Build(v *Variant, vars PromptVars) string {
	// 1. 按顺序渲染每个组件。空 body 被丢掉，于是被省略的 section
	// 其占位符解析为 ""（例如没有服务器时的 MCP）。
	sections := map[string]string{}
	for _, id := range v.ComponentOrder {
		c, ok := b.components[id]
		if !ok {
			continue // 未知组件 id：警告并跳过，和上游一致
		}
		if body := strings.TrimSpace(c.Render(v, vars)); body != "" {
			sections[id] = body
		}
	}

	// 2. 构建占位符 map，低->高优先级：上下文值 < variant 占位符 <
	// 渲染好的 section < 调用方覆盖。
	values := map[string]string{"CWD": vars.CWD, "OS": vars.OS, "SHELL": vars.Shell}
	for k, val := range v.Placeholders {
		values[k] = val
	}
	for id, body := range sections {
		values[id] = body
	}
	for _, id := range v.ComponentOrder { // 被省略的 section 仍要解析为 ""
		if _, ok := values[id]; !ok {
			values[id] = ""
		}
	}
	for k, val := range vars.Placeholders {
		values[k] = val // 调用方覆盖胜出
	}

	// 3. 选模板（显式或自动生成），解析 + 清理。
	tmpl := v.BaseTemplate
	if tmpl == "" {
		tmpl = generateTemplate(v.ComponentOrder)
	}
	return postProcess(resolveTemplate(tmpl, values))
}
```

**4 个非显然之处**：

1. **空 section 仍必须被解析。** 一个返回 `""` 的组件不会进 `sections` map，所以第 2 步显式把它的占位符回填成 `""`。否则在没有服务器的运行里，模板会漏出一个字面量 `{{MCP_SECTION}}`。
2. **未知占位符是保留，而非置空。** `resolveTemplate` 把它解析不了的 `{{TYPO}}` 原样留下。这就是 cline 的"部分解析"规则 —— 一个缺失的 key 会出现在输出里，而不是悄悄消失。
3. **工具注册表是被渲染，而非被引用。** `renderToolSpecs` 遍历 `vars.Tools`，把每个打印成一个 `## <name>` 块；参数排序（required 在前，然后字母序）使得固定输入产出逐字稳定的提示词 —— 这就是快照保证。
4. **拥有"方言"的是 variant，不是 builder。** 同一个 `Build` 对 `generic` 产出 XML 工具列表、对 `next-gen` 产出一条原生工具说明；唯一的差别是 `v.NativeTools` 和 variant 携带的 RULES 覆盖。

## What Changed / 与 s05 的变化

s05 产出的是一次请求的流式*输出*并把它折叠成响应。s06 构建的是*输入*侧 —— 具体说是同一个 `CreateMessageRequest` 的 `System` 字符串和 `Tools` 字段 —— 并且改变了工具被宣告的方式。

```diff
 // s03/s05：工具是一个扁平的 schema 列表，直接交给请求的 `tools` 字段。
-req := CreateMessageRequest{
-    Tools: []ToolSchema{readFile, listFiles}, // 一份扁平的宣告
-}

 // s06：由 variant 决定工具如何被宣告，并且提示词是拼装出来的。
+v := SelectVariant(modelID)                 // generic (XML) | next-gen (native)
+prompt, nativeTools := getSystemPrompt(b, v, PromptVars{Tools: tools, CWD: cwd})
+req := CreateMessageRequest{
+    System: prompt,        // 对 XML variant，工具被渲染进文本
+    Tools:  nativeTools,   // 仅对 native variant 才填充
+}
```

语义上：s03 把工具宣告成一个扁平 schema 列表；s06 把它们嵌进一个结构化、由 variant 驱动的提示词。对 XML variant，`Tools` 字段为空，模型靠读系统字符串学会自己的工具；对 native variant，提示词省掉 XML 块，`Tools` 字段携带规格。cline 两条路都支持，把它们混为一谈是个经典错误 —— s06 用 `Variant.NativeTools` 把这个分叉显式化。

## Try It / 动手试一试

```bash
cd agents/s06-system-prompt

# generic（XML 工具调用）—— 默认。工具以 <tool_name> 块出现。
go run .

# next-gen（原生工具调用）：没有 XML 块；工具列为原生规格。
go run . -variant next-gen

# 由 model id 自动选 variant（claude-4 / gpt-5 / gemini-3 -> next-gen）。
go run . -model claude-opus-4.8

# 包含一个示例的已连接 MCP 服务器 section。
go run . -mcp

# 测试：完全离线，无网络，无 LLM。
go test -v ./...
```

期望输出形态：

```
== system prompt (variant=generic, native_tools=false) ==

You are Cline, a highly skilled software engineer ...

====

TOOL USE
...
# Tools

## read_file
Description: Read the contents of a file at the given path.
Parameters:
- path: (required)
Usage:
<read_file>
<path></path>
</read_file>
...
== request.tools (native specs) ==
(none — tools are embedded in the prompt as XML blocks)
```

切到 `next-gen`，XML 格式块消失、RULES section 改变、`request.tools` 改为列出原生规格。输出是确定性的（没有 LLM 调用），所以跨次运行逐字稳定 —— 见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s06-system-prompt/testdata/expected.txt)。

## Upstream Source Reading / 上游源码阅读

cline 的公共入口是 `apps/vscode/src/core/prompts/system-prompt/index.ts` 里的 `getSystemPrompt(context)`。它向 `PromptRegistry` 单例索要与模型匹配的 variant，并调用其 `build()`；只有当 `context.enableNativeToolCalls` 被设置时，原生工具规格才会一并返回 —— 这正是我们的 `getSystemPrompt` 所建模的 `prompt + nativeTools` 分叉。真正的拼装在 `registry/PromptBuilder.ts` 的 `build()`（一条 `buildComponents → preparePlaceholders → resolve → postProcess` 的三步流水线），variant 由 `PromptRegistry.getModelFamily` 遍历每个 `variant.matcher` 选定，而一个工具变成 prose 是在 `PromptBuilder.tool`。主要差别：cline 有约 12 个 variant、按工具的模型家族覆盖、以及一大段正则 `postProcess`；我们保留两个 variant 和那条 load-bearing 的流水线。

```upstream:apps/vscode/src/core/prompts/system-prompt/index.ts#L13-L21
/**
 * Get the system prompt by id
 */
export async function getSystemPrompt(context: SystemPromptContext) {
	// registry 是单例：它一次性加载所有 variant + component，然后 .get(context)
	// 选出 matcher 适配此模型的 variant 并运行 PromptBuilder
	// （buildComponents -> preparePlaceholders -> resolve -> postProcess）。
	const registry = PromptRegistry.getInstance()
	const systemPrompt = await registry.get(context)
	// 原生工具调用：工具作为结构化规格挂在请求上，而不是 XML 进提示词。
	// 否则是 undefined（XML variant 已把它们渲染进 systemPrompt）。
	// 我们的 Go getSystemPrompt 返回同一个分叉。
	const tools = context.enableNativeToolCalls ? registry.nativeTools : undefined
	return { systemPrompt, tools }
}
```

**对照阅读要点**：

- **单例 registry vs 朴素 builder。** 上游把所有 variant + component 缓存在 `PromptRegistry.getInstance()`；s06 只是用默认 section 构造一个 `PromptBuilder`。思路相同（注册一次、构建多次），只是少了那个全局单例。
- **variant 选择是一个 matcher 循环。** `getModelFamily` 逐个尝试 `variant.matcher(context)`，返回第一个 true，否则回退到 `GENERIC`。我们的 `SelectVariant(modelID)` 就是这个循环，用两条目的表和子串匹配代替 `isNextGenModelFamily`。
- **工具转 prose 自成一个函数。** 上游 `PromptBuilder.tool` 构建 `## <name>` / Description / Parameters / Usage 块（并按 `dependencies`/`contextRequirements` 过滤参数）；我们的 `renderToolSpecs` 产出同样的形态，但不做依赖过滤。
- **`enableNativeToolCalls` 就是我们用 `Variant.NativeTools` 建模的分叉。** 上游根据 context + 模型标志决定 native 还是 XML；我们把它挂在 variant 上，让这一课聚焦在*两种输出形态*，而非标志位的串接。
- **一个故意的简化。** cline 的 `postProcess` 是十来条正则（diff 感知的分隔符处理、空标题移除）。s06 只保留"折叠空 section + trim"，这是我们这套组件实际能产生的全部情形。

**想读更多**：从 `system-prompt/index.ts` 的 `getSystemPrompt`（L16）入手，跟着 `registry.get` 进 `registry/PromptRegistry.ts` 的 `getModelFamily`/`getVariant`，再到 `registry/PromptBuilder.ts` 的 `build`（L23）及其 `tool` 静态方法，最后读一个具体的 section `components/tool_use/index.ts` 和一个 variant `variants/generic/config.ts`。这条线就是 s06 → s09（这些组件为之预留了 MCP section 槽位）的真实代码地图。

---

**下一节预告**：s07 把 s03 在这里宣告过的文件工具演化成**基于 diff 的编辑** —— `replace_in_file` 应用精准的 SEARCH/REPLACE 块、支持流式，而不是整文件重写。
