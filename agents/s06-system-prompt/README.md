# s06 · Modular System Prompt / 模块化系统提示词

> The prompt is assembled from ordered sections, chosen by a model-family variant.
> 提示词由有序的组件拼装而成，由模型家族对应的 variant 决定。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s06`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 6 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

The model only knows what tools exist and how to behave from the **system
prompt**. cline does not hard-code one giant string: a `PromptBuilder` registers
ordered **Section** components (agent role, tool-use format + tool specs,
capabilities, MCP server list, rules, system info), and a **Variant** chosen by
model family decides which sections appear, in what order, and whether tools are
rendered as XML `<tool_name>` blocks (generic) or carried as native tool specs
(next-gen). The tool **registry** from s03 feeds the system **prompt**.

模型只能从**系统提示词**里知道有哪些工具、该怎么行动。cline 不是把一大段字符串写死：
`PromptBuilder` 注册一组有序的 **Section** 组件（角色、工具用法 + 工具规格、能力、
MCP 服务器列表、规则、系统信息），由模型家族对应的 **Variant** 决定哪些 section 出现、
以什么顺序、以及工具是渲染成 XML `<tool_name>` 块（generic）还是作为原生工具规格携带
（next-gen）。s03 的工具**注册表**喂给了系统**提示词**。

```
  SelectVariant(modelID)  ──▶  Variant{ ComponentOrder, NativeTools, overrides }
                                        │
   PromptBuilder.Build(variant, vars)   │  for each id in ComponentOrder:
        render section ──▶ map[id]body   ▼      role · tool_use(+specs) · mcp ·
        resolve {{PLACEHOLDER}} ─────────────▶  capabilities · rules · system_info
        postProcess (drop empty, trim)
                                        │
                                        ▼
              one system-prompt string  +  native tool specs (next-gen only)
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | Canonical `ToolSchema` (the s03 tool shape rendered into the prompt) + wire types carried for continuity |
| `prompt.go` | `Section` interface, `PromptBuilder` (register + `Build`), the six default components, the `{{PLACEHOLDER}}` engine, `postProcess` |
| `variants.go` | `Variant` configs (`generic` XML / `next-gen` native) + `SelectVariant` (first-match-wins by model id) |
| `main.go` | prints the assembled prompt; `-variant`, `-model`, `-mcp` flags; `getSystemPrompt` analogue |
| `prompt_test.go` | 9 tests, fully offline (no network, no LLM) |

---

## Try it / 动手试一试

Tests need no network and no API key / 测试无需联网、无需 API key：

```bash
cd agents/s06-system-prompt
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Print the assembled prompt / 打印拼装好的提示词：

```bash
# generic (XML tool calling) — the default
go run .

# next-gen (native tool calling): no XML block, tools as native specs
go run . -variant next-gen

# auto-select the variant from a model id
go run . -model claude-opus-4.8

# include a sample connected MCP server section
go run . -mcp
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt).
预期输出形态见该文件。

---

## Deliberately omitted / 故意省略

cline ships ~12 variants (gpt-5, gemini-3, xs, hermes, glm, ...), per-tool
model-family overrides with GENERIC fallback, component overrides, version/tag
lookups, and a heavy `postProcess` regex pass. s06 teaches the **shape**:
register ordered sections → pick a variant → resolve placeholders → concatenate.
Two variants and a substring-based model matcher are enough to show XML-vs-native
tool calling without the full registry.

cline 有约 12 个 variant、按工具的模型家族覆盖（带 GENERIC 兜底）、组件覆盖、
版本/标签查找，以及一大段 `postProcess` 正则清理。s06 只教**骨架**：注册有序 section
→ 选 variant → 解析占位符 → 拼接。两个 variant + 子串匹配的 model matcher 足以
讲清 XML 与原生工具调用的差异。

See `docs/en/s06-system-prompt.md` (and `docs/zh/...`) for the full six-section
walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s06-system-prompt.md`。
