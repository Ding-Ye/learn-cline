// Source: apps/vscode/src/integrations/checkpoints/CheckpointGitOperations.ts
// Upstream: cline/cline @ a209825116dca469c80af4be53989638dd329f38
// License: Apache-2.0 (cline is Apache-2.0; this is an annotated, simplified
//          excerpt for teaching. © Cline Bot Inc. Reproduced under Apache-2.0.)
// Permalink:
//   https://github.com/cline/cline/blob/a209825116dca469c80af4be53989638dd329f38/apps/vscode/src/integrations/checkpoints/CheckpointGitOperations.ts#L59-L115
//
// ----------------------------------------------------------------------------
// WHAT THIS IS
// ----------------------------------------------------------------------------
// initShadowGit is where cline's "checkpoints" mechanism is born: it creates a
// SHADOW git repository — a second, hidden repo whose object store lives under
// cline's global storage, but whose WORK-TREE is the user's workspace. That one
// inversion (git dir here, files over there) is what lets cline snapshot the
// workspace after every step and undo any change WITHOUT ever committing to the
// user's real .git history.
//
// The Go port (agents/s10-checkpoints-shadow-git/checkpoint.go) does the same
// thing with `git --git-dir=<shadow> --work-tree=<workspace>` on every os/exec
// call. Upstream instead writes `core.worktree` into the shadow's config (line
// below) so plain `git` commands resolve the work-tree automatically — same
// mechanism, just configured once vs. passed each call.
//
// Upstream uses the `simple-git` library; the underlying git invocations
// (init, addConfig, add, commit) are identical to what we shell out to.
// ----------------------------------------------------------------------------

public async initShadowGit(gitPath: string, cwd: string, taskId: string): Promise<string> {
	Logger.info(`Initializing shadow git`)

	// Nested-repo cleanup: git refuses to track a repo-inside-a-repo without
	// submodules, so cline temporarily renames any nested .git away during
	// staging. Here it undoes any leftover rename from a crashed run.
	// (Our mini omits nested-repo handling entirely — see reading notes.)
	await this.renameNestedGitRepos(false).catch((error) => {
		Logger.warn("CheckpointTracker failed best-effort nested git cleanup during shadow git init:", error)
	})

	// If the shadow already exists, don't re-init — just verify it still points
	// at THIS workspace. The guard "Checkpoints can only be used in the original
	// workspace" stops a shadow from being pointed at the wrong files.
	if (await fileExistsAtPath(gitPath)) {
		const git = simpleGit(path.dirname(gitPath))
		const worktree = await git.getConfig("core.worktree")
		if (worktree.value !== cwd) {
			throw new Error("Checkpoints can only be used in the original workspace: " + worktree.value)
		}
		Logger.warn(`Using existing shadow git at ${gitPath}`)
		await writeExcludesFile(gitPath, await getLfsPatterns(this.cwd))
		return gitPath
	}

	// --- create a brand-new shadow repo --------------------------------------
	const checkpointsDir = path.dirname(gitPath)
	Logger.warn(`Creating new shadow git in ${checkpointsDir}`)

	const git = simpleGit(checkpointsDir)
	await git.init() // the git dir lives in cline's storage, NOT in the workspace

	// THE KEY LINE: point this repo's work-tree at the user's workspace.
	// Now `git add`/`commit` snapshot the workspace, but every object/ref is
	// written under checkpointsDir — invisible to the user's real repo.
	await git.addConfig("core.worktree", cwd)
	// Commits must succeed headlessly: give an identity and disable signing.
	await git.addConfig("commit.gpgSign", "false")
	await git.addConfig("user.name", "Cline Checkpoint")
	await git.addConfig("user.email", "checkpoint@cline.bot")

	// Exclude huge/LFS files so snapshots stay cheap (our mini skips this).
	const lfsPatterns = await getLfsPatterns(cwd)
	await writeExcludesFile(gitPath, lfsPatterns)

	// Stage everything currently in the workspace...
	const addFilesResult = await this.addCheckpointFiles(git) // runs `git add .`
	if (!addFilesResult.success) {
		throw new Error("Failed to add at least one file(s) to checkpoints shadow git")
	}

	// ...and make an initial commit so there is ALWAYS a HEAD to reset back to.
	// --allow-empty: even an empty workspace gets a baseline checkpoint.
	await git.commit("initial commit", { "--allow-empty": null, "--no-verify": null })

	Logger.warn(`Shadow git initialization completed`)
	return gitPath
}

// ----------------------------------------------------------------------------
// THE OTHER TWO HALVES (CheckpointTracker.ts) — the per-turn commit and the undo
// ----------------------------------------------------------------------------
//
// commit()  (CheckpointTracker.ts#L243-L255): after each turn, stage the whole
// workspace and commit, returning the hash the Task records as a checkpoint:
//
//     await this.gitOperations.addCheckpointFiles(git)            // git add .
//     const commitMessage = "checkpoint-" + this.cwdHash + "-" + this.taskId
//     const result = await git.commit(commitMessage, {
//         "--allow-empty": null,    // a no-op turn still produces a checkpoint
//         "--no-verify": null,      // skip the user's hooks in the shadow
//     })
//     const commitHash = (result.commit || "").replace(/^HEAD\s+/, "")
//
// resetHead()  (CheckpointTracker.ts#L364): the undo — a HARD reset moves HEAD
// AND overwrites the work-tree (the user's real files) to that checkpoint:
//
//     await git.reset(["--hard", this.cleanCommitHash(commitHash)])
//
// Our ShadowGit.Commit / ShadowGit.Restore are line-for-line these two calls.
//
// ----------------------------------------------------------------------------
// READING MAP
// ----------------------------------------------------------------------------
// Start here: CheckpointGitOperations.ts → initShadowGit (L59) to see the shadow
// repo created and `core.worktree` set. Then read the lifecycle that drives it
// in CheckpointTracker.ts → create (L127, calls initShadowGit) → commit (L212,
// the per-turn snapshot) → resetHead (L336, the undo). For the safety rails we
// dropped, follow addCheckpointFiles → renameNestedGitRepos (L148) for nested
// repos, CheckpointExclusions.ts for the excludes/LFS file, and
// CheckpointLockUtils.ts for the multi-instance folder lock. That trace is the
// real-source map behind s10.
