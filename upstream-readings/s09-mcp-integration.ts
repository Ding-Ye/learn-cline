// Source: apps/vscode/src/services/mcp/McpHub.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalinks:
//   connectToServer (tools fetch): https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/services/mcp/McpHub.ts#L656-L670
//   fetchToolsList:                https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/services/mcp/McpHub.ts#L677-L711
//   callTool:                      https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/services/mcp/McpHub.ts#L1233-L1308
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// cline's MCP integration, distilled to the three calls s09 reimplements:
//
//   connectToServer (L286)  ← opens a transport, connects a Client, then fetches
//                             the server's tools/resources/prompts (L656-670).
//   fetchToolsList  (L677)  ← issues "tools/list" and maps each tool.
//   callTool        (L1233) ← issues "tools/call" with { name, arguments }.
//
// cline leans on the official @modelcontextprotocol/sdk Client + transports.
// s09 implements the JSON-RPC 2.0 wire by hand (mcp.go) and routes it through a
// Transport interface so a FAKE in-process server stands in for the subprocess.
// ----------------------------------------------------------------------------

// --- connectToServer: after connect, fetch the server's capabilities (L656-670)
// The SDK Client has just finished its own initialize() handshake inside
// client.connect(transport) above this. THEN cline pulls the tool list — the
// same ordering s09's DiscoverTools enforces (Initialize before ListTools).
connection.server.tools = await this.fetchToolsList(name)
connection.server.resources = await this.fetchResourcesList(name)
connection.server.resourceTemplates = await this.fetchResourceTemplatesList(name)
connection.server.prompts = await this.fetchPromptsList(name)
// (s09 only does tools/*; resources & prompts are out of scope.)

// --- fetchToolsList: "tools/list" → McpTool[] (L677-711) ---------------------
private async fetchToolsList(serverName: string): Promise<McpTool[]> {
	try {
		const connection = this.connections.find((conn) => conn.server.name === serverName)
		if (!connection) {
			throw new Error(`No connection found for server: ${serverName}`)
		}
		// Disabled servers have no client, so there are no tools to list.
		if (connection.server.disabled || !connection.client) {
			return []
		}

		// THE request. A typed JSON-RPC call: method "tools/list", validated
		// against ListToolsResultSchema, with a timeout. s09's ListTools is the
		// same call, decoded into []ToolSchema.
		const response = await connection.client.request({ method: "tools/list" }, ListToolsResultSchema, {
			timeout: DEFAULT_REQUEST_TIMEOUT_MS,
		})

		// cline tags each tool with an autoApprove flag read from settings — this
		// is the seam to the approval gate (s04). s09 omits it (no approval yet).
		const autoApproveConfig = /* ...read from mcp settings file... */ [] as string[]
		const tools = (response?.tools || []).map((tool) => ({
			...tool,
			autoApprove: autoApproveConfig.includes(tool.name),
		}))
		return tools
	} catch (error) {
		// On failure, return [] rather than throwing — a flaky server must not
		// take down the whole task. (s09 surfaces the error instead, for clarity.)
		Logger.error(`Failed to fetch tools for ${serverName}:`, error)
		return []
	}
}

// --- callTool: "tools/call" with { name, arguments } (L1233-1308) ------------
async callTool(
	serverName: string,
	toolName: string,
	toolArguments: Record<string, unknown> | undefined,
	ulid: string,
): Promise<McpToolCallResponse> {
	const connection = this.connections.find((conn) => conn.server.name === serverName)
	if (!connection) {
		// A MISSING connection is a hard error (thrown) — distinct from a tool
		// that ran and failed (which comes back as content with isError).
		throw new Error(`No connection found for server: ${serverName}.`)
	}
	if (connection.server.disabled) {
		throw new Error(`Server "${serverName}" is disabled and cannot be used`)
	}

	// Per-server timeout (parsed from config; default DEFAULT_MCP_TIMEOUT_SECONDS).
	// s09 keeps a single transport round-trip; a real timeout would wrap it.
	let timeout = secondsToMs(DEFAULT_MCP_TIMEOUT_SECONDS)
	// ...parse parsedConfig.timeout into `timeout`...

	// THE request: method "tools/call", params { name, arguments }. This is the
	// exact shape s09's callToolParams marshals.
	const result = await connection.client.request(
		{
			method: "tools/call",
			params: {
				name: toolName,
				arguments: toolArguments ?? {}, // never null — the spec wants an object
			},
		},
		CallToolResultSchema,
		{ timeout },
	)

	// Normalize: ensure content is always an array. The { content, isError }
	// shape is what s09's CallToolResult models, and what becomes a tool_result.
	return {
		...result,
		content: result.content ?? [],
	}
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start at connectToServer (L286): see it build a transport (StdioClientTransport
// for "stdio", L384), construct a Client, connect, then call fetchToolsList
// (L657). Follow fetchToolsList (L677) for "tools/list", then callTool (L1233)
// for "tools/call". From there, the discovered tools are rendered into the
// system prompt's tool list (s06) and invoked via UseMcpToolHandler, which routes
// back into McpHub.callTool — that handler is the s03 → s09 bridge: a registry
// entry whose Execute() is a remote call. That trace is the real-source map for
// s09 (this chapter) → s04 (auto-approve tagging) → s06 (tools in the prompt).
