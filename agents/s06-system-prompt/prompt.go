package main

import (
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Modular system-prompt construction.
//
// The model knows nothing about its tools or how to behave except what the
// system prompt tells it. cline does NOT hard-code one giant string: it ASSEMBLES
// the prompt from ordered, reusable Section components, picks the set + their
// order from a Variant chosen by model family, and resolves {{PLACEHOLDER}}
// templates so a section can splice in dynamic content (the tool specs, the MCP
// server list, the cwd).
//
// This file ports that pipeline in miniature. The shape mirrors upstream:
//
//	registry/PromptBuilder.ts  -> PromptBuilder.Build
//	variants/variant-builder.ts -> Variant
//	templates/TemplateEngine.ts -> resolveTemplate
//	templates/placeholders.ts   -> the Section* constants
//	components/*.ts             -> the section functions
// ---------------------------------------------------------------------------

// Section identifiers. These double as the {{PLACEHOLDER}} names a variant's
// baseTemplate uses, exactly like cline's SystemPromptSection enum whose values
// are "AGENT_ROLE_SECTION", "TOOLS_SECTION", etc.
const (
	SectionAgentRole    = "AGENT_ROLE_SECTION"
	SectionToolUse      = "TOOL_USE_SECTION"
	SectionCapabilities = "CAPABILITIES_SECTION"
	SectionRules        = "RULES_SECTION"
	SectionMCP          = "MCP_SECTION"
	SectionSystemInfo   = "SYSTEM_INFO_SECTION"
)

// PromptVars is the build-time context every component reads. It is the s06
// analogue of cline's SystemPromptContext (cwd, mcpHub, supportsBrowserUse, ...).
// Tools are the registered tool schemas (from s03); they are rendered into the
// TOOL_USE section for XML variants or returned as native specs for native ones.
type PromptVars struct {
	CWD          string                 // working directory advertised to the model
	OS           string                 // host OS string for SYSTEM INFORMATION
	Shell        string                 // default shell for SYSTEM INFORMATION
	Tools        []ToolSchema           // registered tools (s03's registry output)
	MCPServers   []MCPServer            // connected MCP servers (s09 preview)
	Placeholders map[string]string      // ad-hoc {{KEY}} overrides (highest priority)
	Extra        map[string]interface{} // escape hatch for component-specific data
}

// MCPServer is the minimal shape the MCP section renders: a name and its tools.
// (s09 builds the real client; here we only need enough to list servers.)
type MCPServer struct {
	Name  string
	Tools []ToolSchema
}

// Section is one prompt component. Render returns the section's text (or "" to
// omit it, e.g. the MCP section when no servers are connected). This is the Go
// equivalent of cline's ComponentFunction `(variant, context) => Promise<string>`.
type Section interface {
	// ID is the placeholder name this section fills (e.g. SectionToolUse).
	ID() string
	// Render produces the section body for the given variant + vars.
	Render(v *Variant, vars PromptVars) string
}

// sectionFunc adapts a plain func into a Section, so simple components can be
// written without a struct (mirrors how cline registers a bare function).
type sectionFunc struct {
	id string
	fn func(v *Variant, vars PromptVars) string
}

func (s sectionFunc) ID() string                                { return s.id }
func (s sectionFunc) Render(v *Variant, vars PromptVars) string { return s.fn(v, vars) }

// NewSection wraps an id + render func into a Section.
func NewSection(id string, fn func(v *Variant, vars PromptVars) string) Section {
	return sectionFunc{id: id, fn: fn}
}

// Variant is a model-family configuration: which sections to include, in what
// order, the base template that lays them out, optional per-section template
// overrides, and native-tool-calling mode. Mirrors cline's PromptVariant built
// by the VariantBuilder.
type Variant struct {
	Family            string            // "generic" | "next-gen" | ...
	Description       string            // human-readable purpose
	ComponentOrder    []string          // section ids, in render order
	BaseTemplate      string            // {{SECTION}} layout; auto-generated if ""
	ComponentOverride map[string]string // sectionID -> override template body
	NativeTools       bool              // true: emit native tool specs, not XML
	Placeholders      map[string]string // variant-level {{KEY}} defaults
}

// ---------------------------------------------------------------------------
// PromptBuilder — registers section components and concatenates them in order.
// ---------------------------------------------------------------------------

// PromptBuilder holds the registered Section components, keyed by id, and the
// variant that selects + orders them. cline's PromptBuilder takes (variant,
// context, components); we take the variant in Build so one builder can render
// any variant against the same component registry.
type PromptBuilder struct {
	components map[string]Section
}

// NewPromptBuilder returns a builder with cline's default component set already
// registered: agent role, tool use (with tool specs), capabilities, MCP, rules,
// system info. Register more with Register before Build.
func NewPromptBuilder() *PromptBuilder {
	b := &PromptBuilder{components: map[string]Section{}}
	for _, s := range defaultSections() {
		b.Register(s)
	}
	return b
}

// Register adds (or replaces) a section component by its id.
func (b *PromptBuilder) Register(s Section) {
	b.components[s.ID()] = s
}

// Build assembles the system prompt for a variant. It (1) renders each component
// in the variant's ComponentOrder, (2) substitutes those rendered sections plus
// any vars/variant placeholders into the base template's {{...}} slots, and
// (3) post-processes whitespace. This is the exact pipeline of cline's
// PromptBuilder.build: buildComponents -> preparePlaceholders -> resolve ->
// postProcess.
func (b *PromptBuilder) Build(v *Variant, vars PromptVars) string {
	// 1. Render each ordered component into a sections map. A component that
	// returns empty (e.g. MCP with no servers) is dropped, so its placeholder
	// resolves to "" — same as cline skipping empty sections.
	sections := map[string]string{}
	for _, id := range v.ComponentOrder {
		c, ok := b.components[id]
		if !ok {
			continue // unknown component id: warn-and-skip, like upstream
		}
		body := strings.TrimSpace(c.Render(v, vars))
		if body != "" {
			sections[id] = body
		}
	}

	// 2. Build the placeholder map. Precedence (low -> high), matching upstream:
	// standard context values < variant placeholders < rendered sections <
	// caller-supplied PromptVars.Placeholders.
	values := map[string]string{
		"CWD":   vars.CWD,
		"OS":    vars.OS,
		"SHELL": vars.Shell,
	}
	for k, val := range v.Placeholders {
		values[k] = val
	}
	for id, body := range sections {
		values[id] = body
	}
	// Any ordered section that rendered empty must still resolve (to "") so the
	// template doesn't leak a literal {{SECTION}} for an intentionally-omitted one.
	for _, id := range v.ComponentOrder {
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

// generateTemplate builds a default layout from the component order: each
// section placeholder separated by a "====" rule, exactly like cline's
// VariantBuilder.generateTemplateFromComponents.
func generateTemplate(order []string) string {
	parts := make([]string, 0, len(order))
	for _, id := range order {
		parts = append(parts, "{{"+id+"}}")
	}
	return strings.Join(parts, "\n\n====\n\n")
}

// placeholderRE matches {{ KEY }} with optional surrounding whitespace.
var placeholderRE = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// resolveTemplate replaces every {{KEY}} with values[KEY]. An UNKNOWN key is
// left intact (the {{KEY}} text stays) so partial resolution is possible and a
// typo is visible rather than silently blanked — this is cline's TemplateEngine
// "keep placeholder if not found" rule.
func resolveTemplate(tmpl string, values map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := strings.TrimSpace(placeholderRE.FindStringSubmatch(match)[1])
		if v, ok := values[key]; ok {
			return v
		}
		return match // unknown placeholder: preserve it
	})
}

// multiBlankRE collapses 3+ consecutive newlines into a paragraph break.
var multiBlankRE = regexp.MustCompile(`\n\s*\n\s*\n+`)

// emptySectionRE matches two "====" rules with only blank space between them,
// which is what an OMITTED section (e.g. MCP with no servers) leaves behind.
var emptySectionRE = regexp.MustCompile(`====\s*\n+\s*====`)

// postProcess cleans up the assembled prompt: drop empty sections left by an
// omitted component, collapse runs of blank lines, strip any leading/trailing
// rule, and trim. This is the (much heavier) regex pass in cline's
// PromptBuilder.postProcess, reduced to the cases s06 can actually produce.
func postProcess(s string) string {
	if s == "" {
		return ""
	}
	// Repeatedly fold "==== <blank> ====" down to a single "====" until stable,
	// so a run of consecutive omitted sections collapses fully.
	for {
		next := emptySectionRE.ReplaceAllString(s, "====")
		if next == s {
			break
		}
		s = next
	}
	s = multiBlankRE.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	// A trailing/leading "====" can survive if the first/last section was omitted.
	s = strings.TrimSuffix(s, "====")
	s = strings.TrimPrefix(s, "====")
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------------------
// Default section components (ports of cline's components/*.ts).
// ---------------------------------------------------------------------------

// defaultSections returns the section set every variant draws from.
func defaultSections() []Section {
	return []Section{
		NewSection(SectionAgentRole, renderAgentRole),
		NewSection(SectionToolUse, renderToolUse),
		NewSection(SectionCapabilities, renderCapabilities),
		NewSection(SectionMCP, renderMCP),
		NewSection(SectionRules, renderRules),
		NewSection(SectionSystemInfo, renderSystemInfo),
	}
}

const agentRoleText = "You are Cline, a highly skilled software engineer with extensive " +
	"knowledge in many programming languages, frameworks, design patterns, and best practices."

// renderAgentRole is cline's agent_role.ts: a fixed identity line, overridable
// per variant via componentOverrides.
func renderAgentRole(v *Variant, vars PromptVars) string {
	if t, ok := v.ComponentOverride[SectionAgentRole]; ok {
		return resolveTemplate(t, mergeVars(vars))
	}
	return agentRoleText
}

// renderToolUse builds the TOOL USE section. For XML variants it emits the
// XML-tag formatting instructions FOLLOWED BY each registered tool rendered as a
// `## <name>` block (this is where s03's schemas become prose). For native
// variants the tools travel as structured specs instead, so the section only
// carries the formatting preamble (a note that tools are called natively).
func renderToolUse(v *Variant, vars PromptVars) string {
	if t, ok := v.ComponentOverride[SectionToolUse]; ok {
		return resolveTemplate(t, mergeVars(vars))
	}

	var b strings.Builder
	b.WriteString("TOOL USE\n\n")
	b.WriteString("You have access to a set of tools that are executed upon the user's approval. ")
	b.WriteString("You can use one tool per message, and will receive the result of that tool use in the user's response. ")
	b.WriteString("You use tools step-by-step to accomplish a given task, with each tool use informed by the result of the previous tool use.\n\n")

	if v.NativeTools {
		// Native variants: the model receives tool specs through the API's
		// `tools` field, so the prompt just states the convention. cline's
		// native variants likewise omit the XML formatting block.
		b.WriteString("# Tool Use\n\n")
		b.WriteString("Tools are provided to you through the native tool-calling interface. ")
		b.WriteString("Call them directly; do not wrap calls in XML tags.")
		return b.String()
	}

	// XML variants: teach the tag format, then list every tool.
	b.WriteString("# Tool Use Formatting\n\n")
	b.WriteString("Tool use is formatted using XML-style tags. The tool name is enclosed in opening and ")
	b.WriteString("closing tags, and each parameter is similarly enclosed within its own set of tags:\n\n")
	b.WriteString("<tool_name>\n<parameter1_name>value1</parameter1_name>\n</tool_name>\n\n")
	b.WriteString("Always adhere to this format for the tool use to ensure proper parsing and execution.\n\n")
	b.WriteString("# Tools\n\n")
	b.WriteString(renderToolSpecs(vars.Tools))
	return b.String()
}

// renderToolSpecs turns a list of ToolSchema into the `## <name>` blocks cline's
// PromptBuilder.tool produces: a title, a Description line, a Parameters list
// (required/optional), and a Usage XML template. The whole point of s06: the
// tool REGISTRY (s03) feeds the system PROMPT.
func renderToolSpecs(tools []ToolSchema) string {
	if len(tools) == 0 {
		return "(No tools available)"
	}
	blocks := make([]string, 0, len(tools))
	for _, t := range tools {
		var b strings.Builder
		b.WriteString("## " + t.Name + "\n")
		b.WriteString("Description: " + t.Description + "\n")

		names, required := toolParams(t.InputSchema)
		if len(names) == 0 {
			b.WriteString("Parameters: None\n")
		} else {
			b.WriteString("Parameters:\n")
			for _, n := range names {
				req := "optional"
				if required[n] {
					req = "required"
				}
				b.WriteString("- " + n + ": (" + req + ")\n")
			}
		}
		// Usage template: the XML skeleton the model fills in.
		b.WriteString("Usage:\n<" + t.Name + ">\n")
		for _, n := range names {
			b.WriteString("<" + n + "></" + n + ">\n")
		}
		b.WriteString("</" + t.Name + ">")
		blocks = append(blocks, b.String())
	}
	return strings.Join(blocks, "\n\n")
}

// toolParams extracts parameter names (sorted for determinism) and a
// required-set from a JSON-Schema-shaped InputSchema:
//
//	{ "properties": { "path": {...} }, "required": ["path"] }
func toolParams(schema map[string]interface{}) ([]string, map[string]bool) {
	required := map[string]bool{}
	if raw, ok := schema["required"].([]string); ok {
		for _, r := range raw {
			required[r] = true
		}
	} else if raw, ok := schema["required"].([]interface{}); ok {
		for _, r := range raw {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}

	var names []string
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for n := range props {
			names = append(names, n)
		}
	}
	// Deterministic order: required first (in their declared-but-sorted order),
	// then the rest, so a fixed input yields a byte-stable prompt (snapshot test).
	sort.Strings(names)
	sort.SliceStable(names, func(i, j int) bool {
		ri, rj := required[names[i]], required[names[j]]
		if ri != rj {
			return ri // required params sort before optional
		}
		return names[i] < names[j]
	})
	return names, required
}

const capabilitiesText = `CAPABILITIES

- You have access to tools that let you execute CLI commands, list files, read and edit files, and search code. These tools help you accomplish a wide range of tasks.
- When the user gives you a task, a recursive list of all filepaths in the current working directory ('{{CWD}}') will be included in environment_details, offering insight into the project's structure.
- You have access to MCP servers that may provide additional tools and resources.`

// renderCapabilities is cline's capabilities.ts (heavily trimmed): a prose list
// of what the agent can do, with {{CWD}} spliced in.
func renderCapabilities(v *Variant, vars PromptVars) string {
	if t, ok := v.ComponentOverride[SectionCapabilities]; ok {
		return resolveTemplate(t, mergeVars(vars))
	}
	return resolveTemplate(capabilitiesText, mergeVars(vars))
}

const rulesText = `RULES

- Your current working directory is: {{CWD}}
- You cannot ` + "`cd`" + ` into a different directory to complete a task. Operate from '{{CWD}}'.
- Do not use ~ or $HOME to refer to the home directory.
- Wait for the user's response after each tool use before proceeding.`

// renderRules is cline's rules.ts (trimmed to the load-bearing few). Variants
// override this wholesale (next-gen ships its own rules_template), which we model
// via ComponentOverride.
func renderRules(v *Variant, vars PromptVars) string {
	if t, ok := v.ComponentOverride[SectionRules]; ok {
		return resolveTemplate(t, mergeVars(vars))
	}
	return resolveTemplate(rulesText, mergeVars(vars))
}

// renderMCP is cline's mcp.ts: lists connected MCP servers and their tools.
// Returns "" when no servers are connected, so the whole section (and its
// surrounding ==== separators) is dropped — the upstream behavior.
func renderMCP(v *Variant, vars PromptVars) string {
	if len(vars.MCPServers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("MCP SERVERS\n\n")
	b.WriteString("The Model Context Protocol (MCP) enables communication with locally running MCP servers ")
	b.WriteString("that provide additional tools and resources.\n\n")
	b.WriteString("# Connected MCP Servers\n")
	for _, s := range vars.MCPServers {
		b.WriteString("\n## " + s.Name + "\n")
		if len(s.Tools) > 0 {
			b.WriteString("### Available Tools\n")
			for _, t := range s.Tools {
				b.WriteString("- " + t.Name + ": " + t.Description + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

const systemInfoText = `SYSTEM INFORMATION

Operating System: {{OS}}
Default Shell: {{SHELL}}
Current Working Directory: {{CWD}}`

// renderSystemInfo is cline's system_info.ts: the host environment block.
func renderSystemInfo(v *Variant, vars PromptVars) string {
	if t, ok := v.ComponentOverride[SectionSystemInfo]; ok {
		return resolveTemplate(t, mergeVars(vars))
	}
	return resolveTemplate(systemInfoText, mergeVars(vars))
}

// mergeVars builds the small placeholder map components use for their own inline
// {{CWD}} / {{OS}} / {{SHELL}} substitutions, with caller overrides applied last.
func mergeVars(vars PromptVars) map[string]string {
	m := map[string]string{
		"CWD":   vars.CWD,
		"OS":    vars.OS,
		"SHELL": vars.Shell,
	}
	for k, v := range vars.Placeholders {
		m[k] = v
	}
	return m
}
