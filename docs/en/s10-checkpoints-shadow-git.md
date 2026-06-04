---
title: "s10 · Checkpoints via Shadow Git"
chapter: 10
slug: s10-checkpoints-shadow-git
est_read_min: 14
---

# s10 · Checkpoints via Shadow Git

> What this teaches: **checkpoints via shadow git** — how cline keeps a second, hidden git repository whose work-tree is your workspace, so it can snapshot after every step and undo any change without ever touching your real git history. This is the capstone mechanism, placed last because it wraps durable per-step undo around everything the loop does.

---

## Problem

By s09 the agent is dangerous in the most useful way: it streams from a real provider (s05), parses tool calls live (s02), dispatches them through a registry (s03) behind an approval gate (s04), applies surgical file edits (s07), and even loads tools from external MCP servers (s09). Every one of those tool calls can change files on disk. A multi-turn run might rewrite a dozen files across ten turns — and then the user wants turn 7 back.

You cannot solve this with the user's own git. Committing after every step would bury their real history under hundreds of `checkpoint-...` commits; stashing fights with their working changes; and many workspaces are not even git repos. You also cannot just copy files to a backup folder — you would reinvent diffing, history, and "restore to point N." The pain is precise: the agent needs a **full, restorable timeline of the workspace, per step, that is completely invisible to the project's real version control**.

## Solution

cline's answer is a **shadow git**: a second git repository that snapshots the workspace, but whose history lives somewhere the user never sees. The whole trick is one inversion of git's two location flags:

```
git --git-dir=<shadowDir> --work-tree=<workspace> <subcommand>
```

`--git-dir` is where the object database, refs, and HEAD live; `--work-tree` is which files the repo tracks. Point the git dir at a hidden directory and the work-tree at the user's workspace, and you get a repo that snapshots their files while writing all its commits *elsewhere*. The user's real `.git` is never created or touched.

Three design decisions carry the chapter:

1. **A separate `--git-dir` is the entire isolation mechanism.** Running `git init` inside the workspace would create `workspace/.git` and start polluting (or colliding with) the user's history. Keeping the git dir outside lets the shadow and the user's repo coexist over the very same files, each blind to the other.
2. **Snapshot = `git add -A` + `git commit --allow-empty`.** Every turn stages all changes and commits, returning a hash the Task records as a `Checkpoint`. `--allow-empty` keeps the undo timeline aligned with turns even when a turn changed nothing.
3. **Undo = `git reset --hard <hash>`.** A hard reset moves HEAD *and* overwrites the work-tree — so the user's real files on disk snap back to that checkpoint. Restore is non-destructive to the timeline: you can keep committing afterward and the chain continues from the restored point.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  every call:  git --git-dir=<shadow> --work-tree=<workspace>    │
│                                                                  │
│   Init    ─▶ git init · config core identity · add -A · commit  │
│   Commit  ─▶ git add -A · git commit --allow-empty ─▶ HASH      │
│   Restore ─▶ git reset --hard HASH ─▶ overwrites workspace files │
│   List    ─▶ git log --format=%H ─▶ [hash, hash, ...]           │
│                                                                  │
│   shadow .git (hidden)            workspace (user's real files)  │
│   ┌──────────────┐   snapshot     ┌────────────────────────┐    │
│   │ objects/refs │◀───────────────│  app.go  util.go  ...   │    │
│   │ HEAD         │───reset──files▶│  (no .git here, ever)   │    │
│   └──────────────┘                └────────────────────────┘    │
└────────────────────────────────────────────────────────────────┘
```

The core ~45-line excerpt (from [`agents/s10-checkpoints-shadow-git/checkpoint.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s10-checkpoints-shadow-git/checkpoint.go)) — init, the per-turn commit, the undo, and the one helper that makes a shadow a shadow:

```go
// Init creates the shadow repo: git dir OUTSIDE the workspace, work-tree = workspace.
func (s *ShadowGit) Init(workdir, shadowDir string) error {
	s.workdir = workdir
	s.shadowDir = shadowDir // NOT workdir/.git — that is the whole point
	if _, err := s.git("init"); err != nil {
		return fmt.Errorf("shadow git init: %w", err)
	}
	for _, kv := range [][2]string{
		{"user.name", "Cline Checkpoint"}, {"user.email", "checkpoint@cline.bot"},
		{"commit.gpgSign", "false"}, // commits must succeed on a bare CI machine
	} {
		if _, err := s.git("config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("shadow git config %s: %w", kv[0], err)
		}
	}
	if _, err := s.git("add", "-A"); err != nil { // stage the current workspace
		return fmt.Errorf("shadow git initial add: %w", err)
	}
	// --allow-empty: there must ALWAYS be a HEAD to reset back to.
	_, err := s.git("commit", "--allow-empty", "--no-verify", "-m", "initial commit")
	return err
}

// Commit snapshots the workspace and returns the new hash (a checkpoint).
func (s *ShadowGit) Commit(message string) (string, error) {
	if _, err := s.git("add", "-A"); err != nil { // new + modified + deleted
		return "", err
	}
	if _, err := s.git("commit", "--allow-empty", "--no-verify", "-m", message); err != nil {
		return "", err
	}
	out, err := s.git("rev-parse", "HEAD") // read back the hash we just wrote
	return strings.TrimSpace(out), err
}

// Restore rewinds the workspace files to an earlier checkpoint (the undo).
func (s *ShadowGit) Restore(hash string) error {
	_, err := s.git("reset", "--hard", hash) // moves HEAD AND the work-tree
	return err
}

// git prepends the two flags that make this a SHADOW git, on EVERY call.
func (s *ShadowGit) git(args ...string) (string, error) {
	full := append([]string{
		"--git-dir=" + s.shadowDir,   // hidden object store
		"--work-tree=" + s.workdir,   // the user's workspace
	}, args...)
	cmd := exec.Command("git", full...)
	// ... capture stdout/stderr, return stderr in the error ...
}
```

**Four non-obvious points**:

1. **The two flags must be on *every* invocation.** The `git` helper is the single chokepoint that guarantees the git dir is always the shadow — there is no code path that could accidentally write to `workspace/.git`. Upstream instead writes `core.worktree` into the shadow's config once, so plain `git` resolves the work-tree; same mechanism, configured vs. passed.
2. **`--allow-empty` is deliberate.** Without it, a turn that changed nothing would error ("nothing to commit") and the checkpoint↔turn alignment would drift. cline always allows empty commits for exactly this reason.
3. **Restore is a *hard* reset, and that is the point.** It overwrites the user's real files on disk. Anything created after the target checkpoint (a new file, a later edit) is removed from the work-tree — that is what "undo to step N" means.
4. **`rev-parse HEAD` reads the hash, not the commit subcommand's output.** Git's `commit` stdout format varies across versions; `git rev-parse HEAD` is the portable way to learn the SHA you just wrote.

## What Changed (vs. s09)

s09 extended the tool *registry* at runtime — it spawned an external MCP server and adapted its remote tools into the executor. s10 sits one layer down: it does not add tools or change the loop's control flow at all. Instead it wraps a **storage layer** around whatever the loop did, so any turn's file changes are restorable.

```diff
 // s09: the registry gains tools from an external process.
-tools := registry.All()                 // compiled-in tools
+remote, _ := hub.ListTools(ctx)          // tools/list over JSON-RPC
+for _, rt := range remote {
+    registry.Register(adaptMCPTool(hub, rt))
+}

 // s10: after each turn, snapshot the workspace; any turn is restorable.
+sg := &ShadowGit{}
+sg.Init(workspace, shadowDir)            // separate --git-dir, work-tree = workspace
+for turn := range loop {
+    // ... run the turn's approved tools (s03/s04), apply edits (s07) ...
+    hash, _ := sg.Commit(fmt.Sprintf("turn %d", turn))  // the checkpoint
+    timeline = append(timeline, Checkpoint{Hash: hash, TurnIndex: turn})
+}
+// the human hits undo:
+sg.Restore(timeline[7].Hash)             // git reset --hard — files snap back
```

The semantic shift: earlier chapters all moved *forward* (parse → dispatch → execute → edit). s10 is the first mechanism that lets the agent move *backward* — a durable, per-step rewind that costs the user's real history nothing.

## Try It

The tests need no network — only a `git` binary on PATH (they `t.Skip` cleanly if git is missing):

```bash
cd agents/s10-checkpoints-shadow-git

# the demo: edit → checkpoint → edit → restore → continue the chain
go run .

# all six tests against a real git binary in a throwaway t.TempDir
go test -v ./...
```

Expected output shape (the hashes and temp paths differ every run — watch the shape):

```
== shadow git checkpoints demo ==
workspace : /tmp/s10-demo-XXXXXX/workspace
shadow dir: /tmp/s10-demo-XXXXXX/shadow-git   (separate GIT_DIR — never the user's .git)
[init] shadow repo created with an initial commit
[turn 1] wrote app.go              -> checkpoint 91e5e0dd
[turn 2] edited app.go + util.go   -> checkpoint 3804a6af
workspace now:
  - app.go
  - util.go
[restore] resetting workspace to checkpoint 91e5e0dd (end of turn 1)
workspace after restore:
  - app.go
app.go contents: "package main\n\nfunc main() {}\n"
[safety] workspace/.git does NOT exist — the user's history was never touched
```

The tells that you ran it right: `util.go` exists after turn 2, then **vanishes** after restoring the turn-1 checkpoint, and the `[safety]` line confirms no `.git` was ever created inside the workspace. If git is absent you instead see "git is not installed" and the tests skip.

## Upstream Source Reading

cline's shadow git lives in two files: `CheckpointGitOperations.ts` does the raw git plumbing (init, stage, commit) and `CheckpointTracker.ts` orchestrates the lifecycle (create → commit → resetHead). The annotated full excerpt is in [`upstream-readings/s10-checkpoints-shadow-git.ts`](https://github.com/Ding-Ye/learn-cline/blob/main/upstream-readings/s10-checkpoints-shadow-git.ts). The heart is `initShadowGit`:

```upstream:apps/vscode/src/integrations/checkpoints/CheckpointGitOperations.ts#L59-L115
public async initShadowGit(gitPath: string, cwd: string, taskId: string): Promise<string> {
	// ... nested-repo cleanup, then: if the shadow exists, just verify worktree ...

	const git = simpleGit(checkpointsDir)
	await git.init() // the git dir lives in cline's storage, NOT in the workspace

	// THE KEY LINE: point this repo's work-tree at the user's workspace.
	// Now `git add`/`commit` snapshot the workspace, but every object/ref is
	// written under checkpointsDir — invisible to the user's real repo.
	await git.addConfig("core.worktree", cwd)
	await git.addConfig("commit.gpgSign", "false")
	await git.addConfig("user.name", "Cline Checkpoint")
	await git.addConfig("user.email", "checkpoint@cline.bot")

	const lfsPatterns = await getLfsPatterns(cwd)
	await writeExcludesFile(gitPath, lfsPatterns) // exclude huge/LFS files

	const addFilesResult = await this.addCheckpointFiles(git) // runs `git add .`
	if (!addFilesResult.success) {
		throw new Error("Failed to add at least one file(s) to checkpoints shadow git")
	}
	// initial commit so there is ALWAYS a HEAD to reset back to
	await git.commit("initial commit", { "--allow-empty": null, "--no-verify": null })
	return gitPath
}
```

**Reading notes**:

- **`core.worktree` vs. our explicit flags**: upstream writes the work-tree into the shadow's config *once* (`addConfig("core.worktree", cwd)`) so plain `git` commands in that repo resolve the workspace automatically. We pass `--work-tree` on every `os/exec` call instead — the same mechanism, just made visible at each call site rather than configured.
- **`simple-git` vs. `os/exec`**: upstream drives git through the `simple-git` library; we shell out to the `git` binary directly. The actual subcommands (`init`, `add .`, `commit --allow-empty --no-verify`, `reset --hard`) are identical — the library is a thin wrapper over the same plumbing.
- **Nested git repos**: upstream temporarily renames any nested `.git` to `.git_disabled` before staging (`renameNestedGitRepos`), because git won't track a repo-inside-a-repo without submodules. Our mini operates on a flat temp workspace and omits this entirely — correct for the teaching case, incomplete for real monorepos.
- **The "original workspace" guard**: re-initializing an existing shadow, upstream checks `core.worktree === cwd` and throws "Checkpoints can only be used in the original workspace." We always create a fresh shadow in the demo/tests, so we don't need the guard — but it's the safety rail that stops a shadow from being pointed at the wrong files.
- **Locks and excludes we kept out on purpose**: real `commit()`/`resetHead()` acquire a folder lock (`tryAcquireCheckpointLockWithRetry`) so two cline instances can't snapshot at once, and init writes an LFS-aware excludes file so huge binaries don't bloat snapshots. Both are correct-but-orthogonal to the core mechanism, so s10 leaves them out.

**Read further**: start at `CheckpointGitOperations.ts` → `initShadowGit` (L59) to see the shadow created and `core.worktree` set, then follow the lifecycle in `CheckpointTracker.ts` → `create` (L127) → `commit` (L212) → `resetHead` (L336). For the safety rails, branch into `addCheckpointFiles` → `renameNestedGitRepos` (L148), `CheckpointExclusions.ts`, and `CheckpointLockUtils.ts`. That trace is the real-source map behind s10.

---

**Next**: this is the final chapter. `s_full-integration` wires s01–s10 into one coherent agent — the loop streams, parses, dispatches, gates, edits, **checkpoints after each turn (this chapter)**, and truncates context, with one tool sourced from MCP. The whole is the cline agent, not a pile of parts.
