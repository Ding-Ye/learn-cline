package main

import "strings"

// ---------------------------------------------------------------------------
// Variants + selection.
//
// cline registers ~12 variants (generic, next-gen, gpt-5, gemini-3, xs, ...),
// each with a matcher(context) predicate. PromptRegistry.getModelFamily walks
// them in order and returns the first whose matcher is true, falling back to
// GENERIC. We teach two variants and the same first-match-wins selection.
//
// - "generic": the fallback. XML tool calling — tools are rendered INTO the
//   prompt as <tool_name> blocks (cline's generic/config.ts).
// - "next-gen": frontier models (Claude 4 / GPT-5 / Gemini). Native tool
//   calling — tools travel as structured specs, the prompt omits the XML block,
//   and the RULES section is overridden with a tighter template (mirrors
//   next-gen/config.ts .overrideComponent(RULES, ...)).
// ---------------------------------------------------------------------------

// nextGenRulesText is the next-gen variant's RULES override — a tightened ruleset
// for smarter models (cline's next-gen/template.ts rules_template, abbreviated).
const nextGenRulesText = `RULES

- Your current working directory is: {{CWD}}; operate from there and do not ` + "`cd`" + ` elsewhere.
- Be concise and direct. Prefer making edits over describing them.
- Wait for the user's response after each tool use before proceeding.`

// GenericVariant is the XML-tools fallback. Component order matches cline's
// generic/config.ts (trimmed to the sections s06 implements).
func GenericVariant() *Variant {
	return &Variant{
		Family:      "generic",
		Description: "The fallback prompt for generic use cases and models (XML tool calling).",
		ComponentOrder: []string{
			SectionAgentRole,
			SectionToolUse,
			SectionMCP,
			SectionCapabilities,
			SectionRules,
			SectionSystemInfo,
		},
		NativeTools:  false,
		Placeholders: map[string]string{"MODEL_FAMILY": "generic"},
	}
}

// NextGenVariant targets frontier models with native tool calling and an
// overridden RULES section (cline's next-gen/config.ts).
func NextGenVariant() *Variant {
	return &Variant{
		Family:      "next-gen",
		Description: "Prompt tailored to frontier models with native tool calling.",
		ComponentOrder: []string{
			SectionAgentRole,
			SectionToolUse,
			SectionMCP,
			SectionCapabilities,
			SectionRules,
			SectionSystemInfo,
		},
		NativeTools: true,
		ComponentOverride: map[string]string{
			SectionRules: nextGenRulesText,
		},
		Placeholders: map[string]string{"MODEL_FAMILY": "next-gen"},
	}
}

// AllVariants returns the registered variants in matcher-priority order:
// specific variants first, GENERIC last as the catch-all (cline orders the same
// way so the fallback only wins when nothing else matched).
func AllVariants() []*Variant {
	return []*Variant{NextGenVariant(), GenericVariant()}
}

// isNextGenModelID reports whether a model id belongs to the next-gen family.
// cline's isNextGenModelFamily checks for Claude 4, GPT-5, Gemini 2.5/3, etc.;
// we keep a small substring table that captures the same intent.
func isNextGenModelID(modelID string) bool {
	id := strings.ToLower(modelID)
	for _, marker := range []string{
		"claude-4", "claude-opus-4", "claude-sonnet-4", "claude-4.",
		"gpt-5", "gemini-2.5", "gemini-3",
	} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

// SelectVariant picks a variant for a model id, first-match-wins over AllVariants
// with GENERIC as the guaranteed fallback. This is PromptRegistry.getVariant +
// getModelFamily collapsed into one function (we don't need the registry's
// version/tag lookups for the lesson).
func SelectVariant(modelID string) *Variant {
	for _, v := range AllVariants() {
		if matches(v, modelID) {
			return v
		}
	}
	return GenericVariant()
}

// matches is the per-variant predicate (cline's variant.matcher). next-gen
// matches frontier model ids; generic matches everything (it is the fallback).
func matches(v *Variant, modelID string) bool {
	switch v.Family {
	case "next-gen":
		return isNextGenModelID(modelID)
	case "generic":
		return true
	default:
		return false
	}
}
