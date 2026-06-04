// Source: apps/vscode/src/core/task/tools/ToolExecutorCoordinator.ts
//   (with the dispatch entry point from apps/vscode/src/core/task/ToolExecutor.ts)
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/ToolExecutorCoordinator.ts#L111-L168
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// The tool registry + dispatcher, distilled. cline splits this across two files:
//
//   ToolExecutorCoordinator.ts  ← the registry: Map<name, handler>, register,
//                                  has, getHandler, execute.
//   ToolExecutor.ts             ← the orchestrator: executeTool(block) is the
//                                  entry the Task loop calls; execute(block)
//                                  (L312) does the has()-check + try/catch.
//
// s03's Go `Registry` (tools.go) IS this Map, and `ToolExecutor.Execute`
// (executor.go) merges the coordinator's execute() with ToolExecutor's
// has()-check, param validation, and error→result handling into one method.
// ----------------------------------------------------------------------------

// --- The registry: a name → handler map (ToolExecutorCoordinator.ts) ---------

export class ToolExecutorCoordinator {
	// THE registry. ~27 handlers live here, keyed by tool name. s03's
	// Registry.handlers is exactly this map.
	private handlers = new Map<string, IToolHandler>()

	// register: store a handler under its own .name. This is what
	// registerToolHandlers() (ToolExecutor.ts L201) calls in a loop over every
	// known tool name to populate the coordinator at task start.
	register(handler: IToolHandler): void {
		this.handlers.set(handler.name, handler)
	}

	// has: is this tool name registered? The orchestrator calls this FIRST so an
	// unknown tool is handled gracefully instead of dispatching into nothing.
	has(toolName: string): boolean {
		return this.getHandler(toolName) !== undefined
	}

	// getHandler: look up the handler. (Real code also remaps MCP tool names to a
	// single handler and lazily builds dynamic subagent handlers — elided here;
	// s03 has neither MCP nor subagents.)
	getHandler(toolName: string): IToolHandler | undefined {
		return this.handlers.get(toolName)
	}

	// execute: resolve the handler and run it. Note this THROWS on an unknown
	// tool — the caller (ToolExecutor.execute, below) is responsible for turning
	// that into a tool_result the model can see, via handleError.
	async execute(config: TaskConfig, block: ToolUse): Promise<ToolResponse> {
		const handler = this.getHandler(block.name)
		if (!handler) {
			throw new Error(`No handler registered for tool: ${block.name}`)
		}
		return handler.execute(config, block)
	}
}

// --- The dispatch entry point (ToolExecutor.ts L312, trimmed) ----------------

// This is what the Task loop actually calls per parsed tool_use block. The big
// idea: a has()-gate, then dispatch, then a try/catch that converts ANY failure
// into a tool_result (handleError → pushToolResult) so the loop never crashes.
private async execute(block: ToolUse): Promise<boolean> {
	if (!this.coordinator.has(block.name)) {
		return false // not our tool — fall through to the legacy path
	}

	try {
		// (omitted here: user-rejection check, "already used a tool this turn"
		//  guard, and plan-mode restrictions — s04 and later chapters.)

		if (block.partial) {
			await this.handlePartialBlock(block, config) // streaming UI; s02 territory
			return true
		}
		await this.handleCompleteBlock(block, config) // ← runs the handler + pushes result
		return true
	} catch (error) {
		// THE safety net: log it, surface it, and push an error tool_result so the
		// model sees what went wrong instead of the loop dying.
		await this.handleError(`executing ${block.name}`, error as Error, block)
		return true
	}
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start at ToolExecutor.ts registerToolHandlers (L201) to see the coordinator
// get populated, then executeTool (L212) → execute (L312) for the per-call
// dispatch. From there:
//
//   ToolExecutor.ts execute (L312)  ← the has()-gate + try/catch (this file)
//     └─▶ ToolExecutorCoordinator.ts execute (L162) ← Map lookup + handler.execute
//           └─▶ handlers/ReadFileToolHandler.ts execute  ← one real tool
//                 └─▶ ToolValidator.ts assertRequiredParams (L17) ← param check
//
// Param validation in s03's executor.go (assertRequiredParams) mirrors that last
// file. The approval gate that wraps handler.execute is ToolExecutor.ts's
// askApproval path → autoApprove.ts shouldAutoApproveTool, which is s04.
// That trace is the real-source map for s03 → s04.
