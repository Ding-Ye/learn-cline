---
title: "s06 · Modular System Prompt"
chapter: 6
slug: s06-system-prompt
est_read_min: 13
---

# s06 · Modular System Prompt

> What this teaches: the **modular system prompt** — how cline assembles the model's instructions from ordered, reusable Section components chosen by a model-family Variant, injecting the tool specs and rules instead of hard-coding one giant string.

---

## Problem

By s05 the agent can stream from a real provider, parse the deltas (s02), dispatch the resulting tool calls through a registry (s03), and gate each one behind approval (s04). But there is a silent assumption underneath all of it: the model already *knows* which tools exist and how it is supposed to behave. It does not. A model knows exactly one thing about its job — whatever the **system prompt** tells it. If the prompt never describes `read_file`, the model will never emit a `read_file` call for s03 to dispatch.

The naive fix is a single hard-coded prompt string with the tools pasted in. That breaks the moment you have more than one model: Claude 4 wants native tool calling, a small local model wants a compact prompt, an XML-only model wants `<tool_name>` blocks. You also can't keep tool descriptions in sync by hand across every variant. cline's answer is to **build** the prompt — compose it from ordered sections, pick the set and order per model family, and render the tool registry into the prompt automatically. This chapter ports that builder in miniature.

## Solution

Treat the system prompt as an *assembly*, not a constant. A `PromptBuilder` registers a set of **Section** components (agent role, tool-use format, capabilities, MCP server list, rules, system info). A **Variant** — chosen by model family — declares which sections to include, in what order, and any per-section overrides. `Build(variant, vars)` renders each section, substitutes `{{PLACEHOLDER}}` slots, and concatenates them.

Three design decisions carry the chapter:

1. **Sections are ordered, composable, and droppable.** The variant's `ComponentOrder` is the single source of truth for layout. A section that renders empty (the MCP section with no servers connected) is dropped, separators and all — so the same component set adapts to context without conditionals at the call site.
2. **The tool registry feeds the prompt.** Each registered `ToolSchema` (s03) is rendered into a `## <name>` block with a Description, a required/optional Parameters list, and a Usage XML skeleton. The list of tools the model *can* call is exactly the registry, never a hand-maintained copy.
3. **A variant selects the dialect.** `generic` renders tools as XML `<tool_name>` blocks inside the prompt; `next-gen` omits the XML block and carries tools as native specs on the request, plus a tighter RULES override. `SelectVariant(modelID)` is first-match-wins with `generic` as the guaranteed fallback — exactly cline's matcher loop.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  SelectVariant("claude-opus-4.8") ─▶ Variant{order, native, …}   │
│                                              │                   │
│  PromptBuilder.Build(variant, vars):         ▼                   │
│    for id in ComponentOrder:                                     │
│       Section.Render ─▶ map[id]=body   role · tool_use(+specs) · │
│                                        mcp? · capabilities ·     │
│    resolve {{CWD}} {{OS}} {{SECTION}}  rules · system_info       │
│       unknown {{KEY}} ─▶ kept intact          │                 │
│    postProcess: drop empty ====, trim         ▼                 │
│                                                                  │
│  ─▶ one system-prompt string  +  native specs (next-gen only)    │
└────────────────────────────────────────────────────────────────┘
```

The core 40-line excerpt (from [`agents/s06-system-prompt/prompt.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s06-system-prompt/prompt.go)) — `Build`, the three-step pipeline that is the whole chapter:

```go
func (b *PromptBuilder) Build(v *Variant, vars PromptVars) string {
	// 1. Render each ordered component. Empty bodies are dropped, so an
	// omitted section's placeholder resolves to "" (e.g. MCP, no servers).
	sections := map[string]string{}
	for _, id := range v.ComponentOrder {
		c, ok := b.components[id]
		if !ok {
			continue // unknown component id: warn-and-skip, like upstream
		}
		if body := strings.TrimSpace(c.Render(v, vars)); body != "" {
			sections[id] = body
		}
	}

	// 2. Build the placeholder map, low->high precedence: context values <
	// variant placeholders < rendered sections < caller overrides.
	values := map[string]string{"CWD": vars.CWD, "OS": vars.OS, "SHELL": vars.Shell}
	for k, val := range v.Placeholders {
		values[k] = val
	}
	for id, body := range sections {
		values[id] = body
	}
	for _, id := range v.ComponentOrder { // omitted sections still resolve to ""
		if _, ok := values[id]; !ok {
			values[id] = ""
		}
	}
	for k, val := range vars.Placeholders {
		values[k] = val // caller overrides win
	}

	// 3. Pick the template (explicit or auto-generated) and resolve + clean it.
	tmpl := v.BaseTemplate
	if tmpl == "" {
		tmpl = generateTemplate(v.ComponentOrder)
	}
	return postProcess(resolveTemplate(tmpl, values))
}
```

**Four non-obvious points**:

1. **An empty section must still resolve.** A component that returns `""` is not in the `sections` map, so step 2 explicitly back-fills its placeholder with `""`. Otherwise the template would leak a literal `{{MCP_SECTION}}` for a server-less run.
2. **Unknown placeholders are kept, not blanked.** `resolveTemplate` leaves a `{{TYPO}}` it can't resolve intact. That is cline's "partial resolution" rule — a missing key shows up in the output instead of silently disappearing.
3. **The tool registry is rendered, not referenced.** `renderToolSpecs` walks `vars.Tools` and prints each as a `## <name>` block; parameters are sorted (required first, then alphabetical) so a fixed input yields a byte-stable prompt — the snapshot guarantee.
4. **The variant, not the builder, owns the dialect.** The same `Build` produces an XML tool list for `generic` and a native-tools note for `next-gen`; the only difference is `v.NativeTools` and the RULES override the variant carries.

## What Changed (vs. s05)

s05 produced the streamed *output* of a request and folded it into a response. s06 builds the *input* side — specifically the `System` string and the `Tools` field of the same `CreateMessageRequest` — and it changes how tools are advertised.

```diff
 // s03/s05: tools were a bare schema list handed to the request's `tools` field.
-req := CreateMessageRequest{
-    Tools: []ToolSchema{readFile, listFiles}, // a flat advertisement
-}

 // s06: a variant decides HOW tools are advertised, and the prompt is assembled.
+v := SelectVariant(modelID)                 // generic (XML) | next-gen (native)
+prompt, nativeTools := getSystemPrompt(b, v, PromptVars{Tools: tools, CWD: cwd})
+req := CreateMessageRequest{
+    System: prompt,        // tools rendered INTO the text for XML variants
+    Tools:  nativeTools,   // populated ONLY for native variants
+}
```

Semantically: s03 advertised tools as a flat schema list; s06 embeds them in a structured, variant-driven prompt. For an XML variant the `Tools` field is empty and the model learns its tools by reading the system string; for a native variant the prompt omits the XML block and the `Tools` field carries the specs. cline supports both paths and conflating them is a classic mistake — s06 makes the fork explicit via `Variant.NativeTools`.

## Try It

```bash
cd agents/s06-system-prompt

# generic (XML tool calling) — the default. Tools appear as <tool_name> blocks.
go run .

# next-gen (native tool calling): no XML block; tools listed as native specs.
go run . -variant next-gen

# auto-select the variant from a model id (claude-4 / gpt-5 / gemini-3 -> next-gen).
go run . -model claude-opus-4.8

# include a sample connected MCP server section.
go run . -mcp

# tests: fully offline, no network, no LLM.
go test -v ./...
```

Expected output shape:

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

For `next-gen` the XML formatting block disappears, the RULES section changes, and `request.tools` lists the native specs instead. Output is deterministic (no LLM call), so it is byte-stable across runs — see [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s06-system-prompt/testdata/expected.txt).

## Upstream Source Reading

cline's public entry is `getSystemPrompt(context)` in `apps/vscode/src/core/prompts/system-prompt/index.ts`. It asks the `PromptRegistry` singleton for the variant matched to the model and calls its `build()`; the native-tool specs come back only when `context.enableNativeToolCalls` is set — exactly the `prompt + nativeTools` fork our `getSystemPrompt` models. The real assembly lives in `registry/PromptBuilder.ts` `build()` (a three-step `buildComponents → preparePlaceholders → resolve → postProcess` pipeline), the variant is chosen by `PromptRegistry.getModelFamily` walking each `variant.matcher`, and a tool becomes prose in `PromptBuilder.tool`. The main difference: cline has ~12 variants, per-tool model-family overrides, and a heavy regex `postProcess`; we keep two variants and the load-bearing pipeline.

```upstream:apps/vscode/src/core/prompts/system-prompt/index.ts#L13-L21
/**
 * Get the system prompt by id
 */
export async function getSystemPrompt(context: SystemPromptContext) {
	// The registry is a singleton: it loads every variant + component once, then
	// .get(context) picks the variant whose matcher fits this model and runs the
	// PromptBuilder (buildComponents -> preparePlaceholders -> resolve -> postProcess).
	const registry = PromptRegistry.getInstance()
	const systemPrompt = await registry.get(context)
	// Native tool calling: the tools travel as STRUCTURED specs on the request,
	// not as XML inside the prompt. Otherwise undefined (the XML variant rendered
	// them into systemPrompt already). Our Go getSystemPrompt returns the same fork.
	const tools = context.enableNativeToolCalls ? registry.nativeTools : undefined
	return { systemPrompt, tools }
}
```

**Reading notes**:

- **Singleton registry vs. plain builder.** Upstream caches every variant + component in a `PromptRegistry.getInstance()`; s06 just constructs a `PromptBuilder` with the default sections. Same idea (register once, build many), minus the global.
- **Variant selection is a matcher loop.** `getModelFamily` tries each `variant.matcher(context)` and returns the first true, falling back to `GENERIC`. Our `SelectVariant(modelID)` is that loop with a two-entry table and a substring matcher instead of `isNextGenModelFamily`.
- **Tool-to-prose is its own function.** Upstream `PromptBuilder.tool` builds the `## <name>` / Description / Parameters / Usage block (and filters params by `dependencies`/`contextRequirements`); our `renderToolSpecs` produces the same shape without the dependency filtering.
- **`enableNativeToolCalls` is the fork we model with `Variant.NativeTools`.** Upstream decides native-vs-XML from context + model flags; we attach it to the variant so the lesson stays about the *two output shapes*, not the flag plumbing.
- **One deliberate simplification.** cline's `postProcess` is a dozen regexes (diff-aware separator handling, empty-header removal). s06 keeps only "collapse empty sections + trim", which is all our component set can actually produce.

**Read further**: start at `system-prompt/index.ts` → `getSystemPrompt` (L16), follow `registry.get` into `registry/PromptRegistry.ts` `getModelFamily`/`getVariant`, then `registry/PromptBuilder.ts` `build` (L23) and its `tool` static, and finally a concrete section in `components/tool_use/index.ts` and a variant in `variants/generic/config.ts`. That trace is the real-source map for s06 → s09 (the MCP section these components reserve a slot for).

---

**Next**: s07 evolves the file tools s03 advertised here into **diff-based editing** — `replace_in_file` applies surgical SEARCH/REPLACE blocks, streaming-aware, instead of rewriting whole files.
