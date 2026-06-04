// Source: apps/vscode/src/core/task/tools/autoApprove.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/core/task/tools/autoApprove.ts#L42-L167
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// This is cline's auto-approve policy — the fast path of the safety model. A
// tool handler asks "can I skip the human?" by calling one of these two methods
// BEFORE it runs. If they return true, the tool executes immediately; if not,
// the handler falls back to askApproval() (UIHelpers.ts L56) and waits for a
// click.
//
// Two methods, two granularities:
//   shouldAutoApproveTool(name)          → the per-tool allowlist (no path)
//   shouldAutoApproveToolWithPath(name)  → allowlist AND a workspace-path check
//
// s04's Go AutoApprovePolicy.shouldAutoApprove (agents/s04-approval-gating/
// approval.go) fuses both into one call, because our toy tools always carry
// their path in params.
// ----------------------------------------------------------------------------

// Returns bool for most tools, and a [local, external] tuple for tools with
// nested settings (reads, edits, bash). The tuple is the whole reason there are
// two methods: the per-tool flag alone can't decide a write — you also need to
// know whether the PATH is inside the workspace.
shouldAutoApproveTool(toolName: ClineDefaultTool): boolean | [boolean, boolean] {
	// Global override #1: "yolo mode" — approve everything, no questions. The
	// switch still lists tools, but every case returns an approve. (s04: Yolo.)
	if (this.stateManager.getGlobalSettingsKey("yoloModeToggled")) {
		switch (toolName) {
			case ClineDefaultTool.FILE_READ:
			case ClineDefaultTool.LIST_FILES:
			// ... reads, edits, bash ...
			case ClineDefaultTool.FILE_EDIT:
			case ClineDefaultTool.BASH:
				return [true, true] // [local, external] both pre-cleared
			case ClineDefaultTool.BROWSER:
			case ClineDefaultTool.MCP_USE:
				return true
		}
	}

	// Global override #2: "approve all" — same effect, separate toggle.
	// (s04: ApproveAll.)
	if (this.stateManager.getGlobalSettingsKey("autoApproveAllToggled")) {
		switch (toolName) {
			case ClineDefaultTool.FILE_READ:
			case ClineDefaultTool.FILE_EDIT:
			case ClineDefaultTool.BASH:
				return [true, true]
			case ClineDefaultTool.MCP_USE:
				return true
		}
	}

	// The per-tool allowlist, read from user settings. THIS is the heart of the
	// policy: which tool categories may skip the human, split into a
	// [insideWorkspace, outsideWorkspace] pair. (s04: ToolPolicy{AutoApprove,
	// AutoApproveExternal}.)
	const autoApprovalSettings = this.stateManager.getGlobalSettingsKey("autoApprovalSettings")
	switch (toolName) {
		case ClineDefaultTool.FILE_READ:
		case ClineDefaultTool.LIST_FILES:
		case ClineDefaultTool.SEARCH:
			// Reads: one flag for local, one for "read files I keep outside the repo".
			return [autoApprovalSettings.actions.readFiles, autoApprovalSettings.actions.readFilesExternally ?? false]
		case ClineDefaultTool.FILE_NEW:
		case ClineDefaultTool.FILE_EDIT:
		case ClineDefaultTool.APPLY_PATCH:
			// Writes: local edits vs. edits that escape the workspace.
			return [autoApprovalSettings.actions.editFiles, autoApprovalSettings.actions.editFilesExternally ?? false]
		case ClineDefaultTool.BASH:
			// Commands: "safe" subset vs. all commands.
			return [autoApprovalSettings.actions.executeSafeCommands ?? false, autoApprovalSettings.actions.executeAllCommands ?? false]
		case ClineDefaultTool.MCP_USE:
			return autoApprovalSettings.actions.useMcp
	}
	return false // DEFAULT: ask a human. Unknown / unlisted tools are never auto-run.
}

// The path-aware decision. Used by file/command tools, where "auto-approve" must
// also depend on WHERE the action lands.
async shouldAutoApproveToolWithPath(
	blockname: ClineDefaultTool,
	autoApproveActionpath: string | undefined,
): Promise<boolean> {
	// Global overrides short-circuit the path check entirely.
	if (this.stateManager.getGlobalSettingsKey("yoloModeToggled")) return true
	if (this.stateManager.getGlobalSettingsKey("autoApproveAllToggled")) return true

	// Decide "is this path inside the workspace?" — the local/external split.
	let isLocalRead = false
	if (autoApproveActionpath) {
		const cwd = await getCwd(getDesktopDir())
		const absolutePath = resolveWorkspacePath(cwd, autoApproveActionpath, "...") as string
		isLocalRead = isLocatedInPath(cwd, absolutePath) // s04: AutoApprovePolicy.isLocal
	} else {
		isLocalRead = false // no path → default to the SAFER answer (external)
	}

	// Pull the [local, external] pair from the per-tool method above.
	const autoApproveResult = this.shouldAutoApproveTool(blockname)
	const [autoApproveLocal, autoApproveExternal] = Array.isArray(autoApproveResult)
		? autoApproveResult
		: [autoApproveResult, false]

	// THE RULE (s04 ports this exact boolean):
	//   local path  → needs the local flag
	//   external    → needs BOTH local AND external flags
	if ((isLocalRead && autoApproveLocal) || (!isLocalRead && autoApproveLocal && autoApproveExternal)) {
		return true
	}
	return false
}

// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// This policy is only HALF of the gate. The other half is the interactive ask:
//
//   task/tools/autoApprove.ts shouldAutoApproveTool / ...WithPath  (this file)
//     └─▶ a tool handler (e.g. WriteToFileToolHandler) checks the above FIRST
//           ├─ true  → run the tool immediately
//           └─ false → task/tools/types/UIHelpers.ts askApproval (L56)
//                        └─▶ task/index.ts ask() (L661) → webview → user clicks
//                              └─ "yesButtonClicked" → run; else → toolDenied
//
// That trace — policy first, askApproval second, denial-as-feedback last — is
// exactly the two-stage ApprovalGate s04 builds. Follow askApproval into ask()
// to see how a single bool is distilled from the webview's rich response.
