package main

import (
	"context"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// ToolExecutor — the dispatcher.
//
// This is the heart of the chapter, distilled from cline's ToolExecutor.execute
// (apps/vscode/src/core/task/ToolExecutor.ts L312-377) plus the coordinator's
// execute (ToolExecutorCoordinator.ts L162-168). The Task loop hands it a parsed
// tool_use block; it looks up the handler, validates params, runs it, and turns
// the outcome into a tool_result. Every failure path STILL returns a
// tool_result (with IsError=true) instead of panicking, so the model always
// gets to see what went wrong — cline does the same in handleError →
// pushToolResult (ToolExecutor.ts L237-243).
// ---------------------------------------------------------------------------

// ToolExecutor registers tools and dispatches parsed tool_use blocks to them.
// (Upstream additionally holds an approver and a coordinator; the approval gate
// is deliberately deferred to s04 — s03 runs every dispatched tool.)
type ToolExecutor struct {
	registry *Registry
}

// NewToolExecutor wraps a populated registry.
func NewToolExecutor(reg *Registry) *ToolExecutor {
	return &ToolExecutor{registry: reg}
}

// GetParam reads a single param from a parsed tool_use block, preferring the
// s02 XML string params, then falling back to the native tool_use Input map.
// This is why the executor doesn't care which front-end (native vs XML) parsed
// the call — both reduce to "name a param, get its string value".
func GetParam(block ToolUse, name string) (string, bool) {
	if block.Params != nil {
		if v, ok := block.Params[name]; ok {
			return v, true
		}
	}
	if block.Input != nil {
		if v, ok := block.Input[name]; ok {
			return fmt.Sprintf("%v", v), true
		}
	}
	return "", false
}

// flatten collapses a parsed block's params (XML + native) into one string map,
// the shape every handler's Execute expects.
func flatten(block ToolUse) map[string]string {
	out := make(map[string]string)
	for k, v := range block.Input {
		out[k] = fmt.Sprintf("%v", v)
	}
	for k, v := range block.Params { // XML params win on conflict
		out[k] = v
	}
	return out
}

// Execute dispatches one parsed tool_use block and returns a ToolResult that is
// always safe to append to the conversation. The control flow mirrors upstream:
//
//  1. registry.Has(name)? — unknown tool → error result (coordinator throws;
//     we'd rather hand the model a result, matching handleError's spirit).
//  2. assertRequiredParams — a missing param is rejected BEFORE Execute runs
//     (ToolValidator.assertRequiredParams, ToolValidator.ts L17-26).
//  3. handler.Execute — run it; any returned error becomes an error result.
//  4. wrap the output (or the error text) as a ToolResult carrying CallID.
func (e *ToolExecutor) Execute(ctx context.Context, block ToolUse) ToolResult {
	// 1. Unknown tool: do not panic, hand the model an error it can recover from.
	handler, ok := e.registry.Get(block.Name)
	if !ok {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error: unknown tool %q. Available tools: %s", block.Name, e.toolNames()),
			IsError: true,
		}
	}

	// 2. Validate required params before doing any work.
	params := flatten(block)
	if missing := assertRequiredParams(handler, params); missing != "" {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error: missing required parameter %q for tool %q.", missing, block.Name),
			IsError: true,
		}
	}

	// 3. Run the tool. A returned error is reported, not thrown.
	out, err := handler.Execute(ctx, params)
	if err != nil {
		return ToolResult{
			CallID:  block.CallID,
			Output:  fmt.Sprintf("Error executing %s: %v", block.Name, err),
			IsError: true,
		}
	}

	// 4. Success.
	return ToolResult{CallID: block.CallID, Output: out}
}

// assertRequiredParams returns the name of the first required param that is
// missing or blank, or "" if all are present (ToolValidator.assertRequiredParams).
func assertRequiredParams(h ToolHandler, params map[string]string) string {
	for _, p := range h.RequiredParams() {
		if strings.TrimSpace(params[p]) == "" {
			return p
		}
	}
	return ""
}

// toolNames lists registered tool names for an unknown-tool error message.
func (e *ToolExecutor) toolNames() string {
	schemas := e.registry.Schemas()
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}
