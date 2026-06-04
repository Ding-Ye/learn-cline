// Command s06 demonstrates cline's modular, model-aware system-prompt
// construction. A PromptBuilder registers ordered Section components (agent
// role, tool use + tool specs, capabilities, MCP, rules, system info) and a
// Variant chosen by model family decides which sections appear, in what order,
// and whether tools are rendered as XML blocks or carried as native specs.
//
//	go run .                              # default: generic (XML) variant
//	go run . -variant next-gen           # native-tool variant
//	go run . -model claude-opus-4.8      # auto-select by model id
//	go run . -mcp                        # include a sample MCP server section
package main

import (
	"flag"
	"fmt"
	"os"
)

// getSystemPrompt is the s06 analogue of cline's getSystemPrompt(context): build
// the prompt string for the chosen variant, and return the native tool specs
// only when the variant uses native tool calling (otherwise tools live inside
// the prompt text and the request's `tools` field stays empty).
func getSystemPrompt(b *PromptBuilder, v *Variant, vars PromptVars) (prompt string, nativeTools []ToolSchema) {
	prompt = b.Build(v, vars)
	if v.NativeTools {
		nativeTools = vars.Tools
	}
	return prompt, nativeTools
}

// demoTools is a small registered tool set (the kind s03 holds), used to show
// how schemas become prompt prose.
func demoTools() []ToolSchema {
	return []ToolSchema{
		{
			Name:        "read_file",
			Description: "Read the contents of a file at the given path.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
				"required":   []string{"path"},
			},
		},
		{
			Name:        "execute_command",
			Description: "Run a CLI command in the working directory.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command":           map[string]interface{}{"type": "string"},
					"requires_approval": map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"command"},
			},
		},
	}
}

func main() {
	variantName := flag.String("variant", "", "force a variant: generic | next-gen (overrides -model selection)")
	model := flag.String("model", "claude-3-5-sonnet", "model id used to auto-select a variant")
	withMCP := flag.Bool("mcp", false, "include a sample connected MCP server in the prompt")
	flag.Parse()

	// Variant: explicit flag wins; otherwise auto-select from the model id.
	var v *Variant
	switch *variantName {
	case "generic":
		v = GenericVariant()
	case "next-gen":
		v = NextGenVariant()
	case "":
		v = SelectVariant(*model)
	default:
		fmt.Fprintf(os.Stderr, "unknown variant %q (use generic|next-gen)\n", *variantName)
		os.Exit(1)
	}

	vars := PromptVars{
		CWD:   "/Users/dev/project",
		OS:    "macOS",
		Shell: "/bin/zsh",
		Tools: demoTools(),
	}
	if *withMCP {
		vars.MCPServers = []MCPServer{{
			Name: "weather",
			Tools: []ToolSchema{
				{Name: "get_forecast", Description: "Get the weather forecast for a city."},
			},
		}}
	}

	b := NewPromptBuilder()
	prompt, nativeTools := getSystemPrompt(b, v, vars)

	fmt.Printf("== system prompt (variant=%s, native_tools=%v) ==\n\n", v.Family, v.NativeTools)
	fmt.Println(prompt)
	fmt.Printf("\n== request.tools (native specs) ==\n")
	if len(nativeTools) == 0 {
		fmt.Println("(none — tools are embedded in the prompt as XML blocks)")
	} else {
		for _, t := range nativeTools {
			fmt.Printf("- %s: %s\n", t.Name, t.Description)
		}
	}
}
