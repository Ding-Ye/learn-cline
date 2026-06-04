// Source: apps/vscode/src/core/prompts/system-prompt/registry/PromptBuilder.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/prompts/system-prompt/registry/PromptBuilder.ts#L23-L84
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// This is the heart of cline's modular system-prompt construction. The public
// entry getSystemPrompt(context) (system-prompt/index.ts L16) just asks the
// PromptRegistry for the right variant and calls THIS class's build(). build()
// is a three-step pipeline:
//
//   1. buildComponents()    — run each ordered component fn, collect its text
//   2. preparePlaceholders() — gather section bodies + context vars into a map
//   3. templateEngine.resolve(baseTemplate, ...) + postProcess()
//
// The Go port (agents/s06-system-prompt/prompt.go) mirrors this exactly:
//   PromptBuilder.Build  -> render sections, build placeholder map, resolve, clean
//   Section.Render        -> componentFn(variant, context)
//   resolveTemplate       -> TemplateEngine.resolve ("keep placeholder if not found")
//   defaultSections()     -> components/index.ts getSystemPromptComponents()
// ----------------------------------------------------------------------------

export class PromptBuilder {
	private templateEngine: TemplateEngine

	constructor(
		private variant: PromptVariant, // which sections, in what order, + overrides
		private context: SystemPromptContext, // cwd, mcpHub, model info, flags
		private components: ComponentRegistry, // id -> componentFn map
	) {
		this.templateEngine = new TemplateEngine()
	}

	// The whole construction in three lines: render components, fill the template
	// with them, clean up. Our Go Build() is the same three steps inlined.
	async build(): Promise<string> {
		const componentSections = await this.buildComponents()
		const placeholderValues = this.preparePlaceholders(componentSections)
		const prompt = this.templateEngine.resolve(this.variant.baseTemplate, this.context, placeholderValues)
		return this.postProcess(prompt)
	}

	// buildComponents runs each component IN ORDER (componentOrder), so the prompt
	// is deterministic and sections appear where the variant wants them. A
	// component that throws or returns blank is skipped — its placeholder then
	// resolves to "" and the section silently drops (e.g. MCP with no servers).
	private async buildComponents(): Promise<Record<string, string>> {
		const sections: Record<string, string> = {}
		const { componentOrder } = this.variant

		for (const componentId of componentOrder) {
			const componentFn = this.components[componentId]
			if (!componentFn) {
				Logger.warn(`Warning: Component '${componentId}' not found`)
				continue // unknown id: warn-and-skip (our Go Build does the same)
			}
			try {
				// componentFn === our Section.Render(variant, vars). It returns the
				// section body, splicing in dynamic content (tool specs, cwd, ...).
				const result = await componentFn(this.variant, this.context)
				if (result?.trim()) {
					sections[componentId] = result // blank result => omitted section
				}
			} catch (error) {
				Logger.warn(`Warning: Failed to build component '${componentId}':`, error)
			}
		}
		return sections
	}

	// preparePlaceholders layers values low->high priority: variant placeholders,
	// then standard context values (cwd, model family, current date), then the
	// rendered section bodies, then runtime overrides. resolve() substitutes every
	// {{KEY}} from this map; an unknown {{KEY}} is LEFT INTACT (partial
	// resolution), which is why a typo shows up rather than blanking silently.
	private preparePlaceholders(componentSections: Record<string, string>): Record<string, unknown> {
		const placeholders: Record<string, unknown> = {}
		Object.assign(placeholders, this.variant.placeholders)
		placeholders[STANDARD_PLACEHOLDERS.CWD] = this.context.cwd || process.cwd()
		placeholders[STANDARD_PLACEHOLDERS.MODEL_FAMILY] = this.variant.family
		placeholders[STANDARD_PLACEHOLDERS.CURRENT_DATE] = new Date().toISOString().split("T")[0]
		Object.assign(placeholders, componentSections) // section bodies win over defaults
		return placeholders
	}

	// ----------------------------------------------------------------------------
	// tool() — how a registered tool BECOMES prompt prose. This is the bridge from
	// s03's tool registry to s06's system prompt: each ClineToolSpec is rendered as
	// a "## <name>" block with Description, a Parameters list (required/optional),
	// and a Usage XML skeleton. Our Go renderToolSpecs() produces the same shape.
	// (Excerpted from PromptBuilder.tool / buildParametersSection / buildUsageSection.)
	// ----------------------------------------------------------------------------
	public static tool(config: ClineToolSpec, registry: ClineDefaultTool[], context: SystemPromptContext): string {
		const displayName = config.name || config.id
		const title = `## ${displayName}`
		const description = [`Description: ${config.description}`]

		const params = [...(config.parameters ?? [])]
		const paramList = params.map((p) => {
			const requiredText = p.required ? "required" : "optional"
			return `- ${p.name}: (${requiredText}) ${resolveInstruction(p.instruction, context)}`
		})
		const parametersSection = params.length ? ["Parameters:", ...paramList].join("\n") : "Parameters: None"

		// Usage: the XML skeleton the model fills in — <tool><param></param></tool>.
		const usage = [`<${displayName}>`]
		for (const param of params) {
			usage.push(`<${param.name}>${param.usage || ""}</${param.name}>`)
		}
		usage.push(`</${displayName}>`)

		return [title, description.join("\n"), parametersSection, ["Usage:", ...usage].join("\n")].join("\n")
	}
}

// ----------------------------------------------------------------------------
// VARIANT SELECTION (system-prompt/registry/PromptRegistry.ts getModelFamily)
// ----------------------------------------------------------------------------
// build() needs a variant; the registry picks it by walking every registered
// variant's matcher(context) and taking the FIRST that returns true, falling
// back to GENERIC. Our Go SelectVariant(modelID) is this loop, collapsed.
//
//   getModelFamily(context) {
//     for (const [_, v] of this.variants.entries()) {
//       if (v.matcher(context)) return v.family   // first match wins
//     }
//     return ModelFamily.GENERIC                   // guaranteed fallback
//   }
//
// generic/config.ts .matcher returns true for "everything not matched above"
// (the fallback); next-gen/config.ts .matcher returns true for frontier model
// ids (isNextGenModelFamily). next-gen also .overrideComponent(RULES, ...) — our
// Variant.ComponentOverride[SectionRules] models that exact override.

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start at the public entry, then descend into the builder and the pieces it
// pulls together:
//
//   system-prompt/index.ts getSystemPrompt (L16)          ← entry: registry.get(context)
//     └─▶ registry/PromptRegistry.ts get / getModelFamily ← variant selection (matchers)
//           └─▶ registry/PromptBuilder.ts build (L23)      ← this file: the 3-step pipeline
//                 ├─▶ components/index.ts getSystemPromptComponents ← the section fns
//                 │     └─▶ components/{agent_role,tool_use,mcp,rules,system_info}.ts
//                 ├─▶ registry/PromptBuilder.ts tool        ← schema -> "## <name>" prose
//                 └─▶ templates/TemplateEngine.ts resolve    ← {{PLACEHOLDER}} substitution
//                       └─▶ variants/variant-builder.ts      ← how a variant is declared
//
// That trace is the real-source map for s06: ordered components, a variant that
// orders them, a template engine that fills them. It builds the INPUT side of the
// request whose streamed OUTPUT s05 learned to decode, and it advertises the
// tools that s03 registered + s04 gates.
