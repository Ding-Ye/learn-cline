package main

import (
	"strings"
	"testing"
)

// testTools is a fixed tool set reused across tests for deterministic prompts.
func testTools() []ToolSchema {
	return []ToolSchema{
		{
			Name:        "read_file",
			Description: "Read a file at the given path.",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
				"required":   []string{"path"},
			},
		},
		{
			Name:        "list_files",
			Description: "List files in a directory.",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{
					"path":      map[string]interface{}{"type": "string"},
					"recursive": map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"path"},
			},
		},
	}
}

func baseVars() PromptVars {
	return PromptVars{CWD: "/work", OS: "macOS", Shell: "/bin/zsh", Tools: testTools()}
}

// Test 1: Build includes every ordered section, in the variant's order.
func TestBuildIncludesAllSectionsInOrder(t *testing.T) {
	b := NewPromptBuilder()
	v := GenericVariant()
	got := b.Build(v, baseVars())

	// The headline text each section emits, in the order the variant declares.
	wantOrder := []string{
		"You are Cline",      // AGENT_ROLE
		"TOOL USE",           // TOOL_USE
		"CAPABILITIES",       // CAPABILITIES (MCP omitted: no servers)
		"RULES",              // RULES
		"SYSTEM INFORMATION", // SYSTEM_INFO
	}
	last := -1
	for _, marker := range wantOrder {
		idx := strings.Index(got, marker)
		if idx == -1 {
			t.Fatalf("section marker %q missing from prompt", marker)
		}
		if idx <= last {
			t.Fatalf("section %q out of order (index %d <= previous %d)\n%s", marker, idx, last, got)
		}
		last = idx
	}
}

// Test 2: Tool descriptions are injected from the registered schemas.
func TestToolDescriptionsInjectedFromSchemas(t *testing.T) {
	b := NewPromptBuilder()
	got := b.Build(GenericVariant(), baseVars())

	for _, want := range []string{
		"## read_file",
		"Description: Read a file at the given path.",
		"## list_files",
		"Description: List files in a directory.",
		"- path: (required)",
		"- recursive: (optional)",
		"<read_file>",   // usage skeleton
		"<path></path>", // parameter usage tag
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing tool detail %q\n---\n%s", want, got)
		}
	}
}

// Test 3: The RULES section is present (and CWD is substituted into it).
func TestRulesSectionPresent(t *testing.T) {
	b := NewPromptBuilder()
	got := b.Build(GenericVariant(), baseVars())

	if !strings.Contains(got, "RULES") {
		t.Fatal("RULES section absent from prompt")
	}
	if !strings.Contains(got, "Your current working directory is: /work") {
		t.Errorf("RULES section did not substitute {{CWD}}\n%s", got)
	}
}

// Test 4: Variant selection changes the output (XML block vs native note, and
// the overridden RULES template).
func TestVariantChangesOutput(t *testing.T) {
	b := NewPromptBuilder()
	generic := b.Build(GenericVariant(), baseVars())
	nextGen := b.Build(NextGenVariant(), baseVars())

	if generic == nextGen {
		t.Fatal("generic and next-gen variants produced identical prompts")
	}

	// generic uses XML tool formatting; next-gen uses native tool calling.
	if !strings.Contains(generic, "Tool Use Formatting") {
		t.Error("generic variant missing XML 'Tool Use Formatting' block")
	}
	if !strings.Contains(generic, "## read_file") {
		t.Error("generic variant should render tool specs inline")
	}
	if strings.Contains(nextGen, "Tool Use Formatting") {
		t.Error("next-gen variant must NOT include the XML formatting block")
	}
	if !strings.Contains(nextGen, "native tool-calling interface") {
		t.Error("next-gen variant missing the native-tools note")
	}
	// next-gen overrides RULES with its own tighter template.
	if !strings.Contains(nextGen, "Be concise and direct") {
		t.Error("next-gen variant did not apply its RULES override")
	}
	if strings.Contains(nextGen, "Do not use ~ or $HOME") {
		t.Error("next-gen variant leaked the generic RULES text")
	}
}

// Test 5: Placeholder substitution — known keys resolve, unknown keys are kept
// intact (cline's "keep placeholder if not found" rule), caller vars win.
func TestPlaceholderSubstitution(t *testing.T) {
	values := map[string]string{"CWD": "/srv", "OS": "linux"}

	if got := resolveTemplate("dir={{CWD}} os={{OS}}", values); got != "dir=/srv os=linux" {
		t.Errorf("known placeholders not resolved: %q", got)
	}
	// Unknown placeholder preserved verbatim.
	if got := resolveTemplate("x={{MISSING}}", values); got != "x={{MISSING}}" {
		t.Errorf("unknown placeholder should be preserved, got %q", got)
	}
	// Whitespace inside braces is tolerated.
	if got := resolveTemplate("a={{ CWD }}", values); got != "a=/srv" {
		t.Errorf("whitespace placeholder not resolved: %q", got)
	}

	// Caller-supplied PromptVars.Placeholders override defaults at Build time.
	vars := baseVars()
	vars.Placeholders = map[string]string{"CWD": "/override"}
	got := NewPromptBuilder().Build(GenericVariant(), vars)
	if !strings.Contains(got, "Current Working Directory: /override") {
		t.Errorf("caller placeholder override not applied\n%s", got)
	}
}

// Test 6: Model id auto-selects the right variant.
func TestSelectVariantByModelID(t *testing.T) {
	cases := []struct {
		modelID string
		family  string
	}{
		{"claude-opus-4.8", "next-gen"},
		{"claude-sonnet-4-20250514", "next-gen"},
		{"gpt-5", "next-gen"},
		{"gemini-3-pro", "next-gen"},
		{"claude-3-5-sonnet", "generic"}, // not next-gen -> fallback
		{"llama-3-8b", "generic"},
		{"", "generic"}, // unknown/empty -> fallback
	}
	for _, c := range cases {
		if got := SelectVariant(c.modelID).Family; got != c.family {
			t.Errorf("SelectVariant(%q) = %q, want %q", c.modelID, got, c.family)
		}
	}
}

// Test 7: The MCP section is omitted when no servers are connected and included
// (with its tools) when they are — and the empty case leaves no dangling rule.
func TestMCPSectionConditional(t *testing.T) {
	b := NewPromptBuilder()

	// No servers: section omitted, and no "====\n\n====" double-rule left behind.
	noMCP := b.Build(GenericVariant(), baseVars())
	if strings.Contains(noMCP, "MCP SERVERS") {
		t.Error("MCP section should be omitted when no servers are connected")
	}
	if strings.Contains(noMCP, "====\n\n====") {
		t.Errorf("omitted MCP section left a dangling double separator\n%s", noMCP)
	}

	// With a server: section present, lists the server and its tools.
	vars := baseVars()
	vars.MCPServers = []MCPServer{{
		Name:  "weather",
		Tools: []ToolSchema{{Name: "get_forecast", Description: "Forecast for a city."}},
	}}
	withMCP := b.Build(GenericVariant(), vars)
	for _, want := range []string{"MCP SERVERS", "## weather", "- get_forecast: Forecast for a city."} {
		if !strings.Contains(withMCP, want) {
			t.Errorf("MCP prompt missing %q\n%s", want, withMCP)
		}
	}
}

// Test 8: Determinism — a fixed variant + vars yields a byte-identical prompt
// across builds (snapshot guarantee, cline's snapshot tests).
func TestDeterministicOutput(t *testing.T) {
	b := NewPromptBuilder()
	v := GenericVariant()
	first := b.Build(v, baseVars())
	for i := 0; i < 20; i++ {
		if got := b.Build(v, baseVars()); got != first {
			t.Fatalf("Build is not deterministic on run %d", i)
		}
	}
}

// Test 9: getSystemPrompt returns native tool specs only for native variants.
func TestGetSystemPromptNativeTools(t *testing.T) {
	b := NewPromptBuilder()

	_, generic := getSystemPrompt(b, GenericVariant(), baseVars())
	if len(generic) != 0 {
		t.Errorf("generic (XML) variant should return no native tools, got %d", len(generic))
	}

	_, native := getSystemPrompt(b, NextGenVariant(), baseVars())
	if len(native) != len(testTools()) {
		t.Errorf("next-gen variant should return %d native tools, got %d", len(testTools()), len(native))
	}
}
