package main

// checkpoint.go — a shadow git, cline's per-step undo.
//
// THE BIG IDEA
// ------------
// An autonomous agent edits files. The user needs to undo any step without
// thinking about it — but the agent must NOT pollute the user's real git
// history with a commit after every keystroke. cline's answer is a "shadow"
// git: a SECOND, hidden git repository whose work-tree is the user's workspace
// but whose .git directory lives somewhere else entirely. It snapshots the
// workspace after each turn; restoring is just `git reset --hard <hash>`.
//
// The whole trick is two git flags used together:
//
//	git --git-dir=<shadowDir> --work-tree=<workdir> ...
//
//	--git-dir   : where the object database / refs / HEAD live (the shadow).
//	--work-tree : which files the repo tracks (the user's actual workspace).
//
// Because --git-dir points at the shadow and NOT at <workdir>/.git, none of
// these commits ever appear in `git log` for the user's repo. cline gets a full
// undo timeline that is completely invisible to the project's real history.
// (Upstream goes further — it writes core.worktree into the shadow's config so
// plain `git` commands resolve the work-tree automatically; we pass the flag
// explicitly on every call, which is the same mechanism made obvious.)

import (
	"fmt"
	"os/exec"
	"strings"
)

// ShadowGit drives a single shadow repository.
//
// Mirrors the pairing of CheckpointTracker (lifecycle: create/commit/resetHead)
// and GitOperations (the raw git plumbing: init, add, commit) in
// apps/vscode/src/integrations/checkpoints/. Here both collapse into one small
// struct because we don't need locks, telemetry, or nested-repo handling to
// teach the mechanism.
type ShadowGit struct {
	workdir   string // the work-tree: the user's workspace (real files live here)
	shadowDir string // the --git-dir: the hidden object store (NOT workdir/.git)
}

// Init creates (or re-attaches to) the shadow repository.
//
// workdir is the workspace whose files we snapshot; shadowDir is where the
// shadow's git database lives. Crucially shadowDir is OUTSIDE workdir/.git, so
// the user's real repository is never touched. After init the shadow has one
// initial (possibly empty) commit so there is always a HEAD to diff and reset
// against — exactly like upstream's `git commit "initial commit" --allow-empty`
// in initShadowGit.
//
// WHY a separate --git-dir: if we ran `git init` inside workdir we'd create
// workdir/.git and start committing the user's files into THEIR history (or
// collide with an existing repo). Keeping the git dir elsewhere means the
// shadow and the user's repo can coexist over the very same files, each blind
// to the other.
func (s *ShadowGit) Init(workdir, shadowDir string) error {
	s.workdir = workdir
	s.shadowDir = shadowDir

	// `git init` with an explicit --git-dir places the object store at
	// shadowDir instead of the default <workdir>/.git. --work-tree tells git
	// that the tracked files live in workdir.
	if _, err := s.git("init"); err != nil {
		return fmt.Errorf("shadow git init: %w", err)
	}

	// Identity + no-gpg config so commits succeed in a bare CI environment
	// (upstream sets the same: user.name "Cline Checkpoint", commit.gpgSign
	// false). Without an identity, `git commit` errors on a clean machine.
	for _, kv := range [][2]string{
		{"user.name", "Cline Checkpoint"},
		{"user.email", "checkpoint@cline.bot"},
		{"commit.gpgSign", "false"},
	} {
		if _, err := s.git("config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("shadow git config %s: %w", kv[0], err)
		}
	}

	// Stage everything currently in the workspace and make an initial commit.
	// --allow-empty so init works even on an empty workspace; there must
	// always be a HEAD to reset back to.
	if _, err := s.git("add", "-A"); err != nil {
		return fmt.Errorf("shadow git initial add: %w", err)
	}
	if _, err := s.git("commit", "--allow-empty", "--no-verify", "-m", "initial commit"); err != nil {
		return fmt.Errorf("shadow git initial commit: %w", err)
	}
	return nil
}

// Commit snapshots the current state of the workspace and returns the new
// commit hash, which the caller records as a Checkpoint.
//
// This is the per-turn snapshot: `git add -A` stages every change (new,
// modified, deleted), then `git commit` writes it into the shadow. --allow-empty
// means a turn that changed nothing still produces a checkpoint (so the undo
// timeline stays aligned with turns). Mirrors CheckpointTracker.commit's
// addCheckpointFiles + git.commit(message, {"--allow-empty", "--no-verify"}).
func (s *ShadowGit) Commit(message string) (string, error) {
	if _, err := s.git("add", "-A"); err != nil {
		return "", fmt.Errorf("shadow git add: %w", err)
	}
	if _, err := s.git("commit", "--allow-empty", "--no-verify", "-m", message); err != nil {
		return "", fmt.Errorf("shadow git commit: %w", err)
	}
	// `git rev-parse HEAD` is the portable way to read back the hash we just
	// wrote (the commit subcommand's stdout format varies across git versions).
	out, err := s.git("rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("shadow git rev-parse: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Restore rewinds the workspace to the state captured by an earlier checkpoint.
//
// `git reset --hard <hash>` moves HEAD to the target commit AND overwrites the
// work-tree (the user's real files) to match. Because --work-tree points at the
// workspace, the user's actual files change on disk — that is the undo. This is
// CheckpointTracker.resetHead: git.reset(["--hard", commitHash]).
//
// Note resetHead is a HARD reset: anything done after <hash> is discarded from
// the working tree. The shadow can keep committing afterwards, so restore-then-
// edit-then-commit simply continues the chain from the restored point.
func (s *ShadowGit) Restore(hash string) error {
	if _, err := s.git("reset", "--hard", hash); err != nil {
		return fmt.Errorf("shadow git reset --hard %s: %w", hash, err)
	}
	return nil
}

// List returns the shadow's commit hashes, newest first.
//
// `git log --format=%H` prints one full SHA per line. The caller pairs these
// with the Checkpoints it recorded to present an undo timeline.
func (s *ShadowGit) List() ([]string, error) {
	out, err := s.git("log", "--format=%H")
	if err != nil {
		return nil, fmt.Errorf("shadow git log: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// git runs one git subcommand against the shadow repository.
//
// Every call prepends the two flags that make a "shadow" git a shadow:
//
//	--git-dir=<shadowDir>    the hidden object store (not workdir/.git)
//	--work-tree=<workdir>    the user's workspace as the tracked tree
//
// This is the single chokepoint that guarantees we never touch the user's real
// .git: the git dir is ALWAYS the shadow. On failure we return stderr in the
// error so test output is legible. (We shell out via os/exec — upstream uses
// the simple-git library, but the underlying git invocations are identical.)
func (s *ShadowGit) git(args ...string) (string, error) {
	full := append([]string{
		"--git-dir=" + s.shadowDir,
		"--work-tree=" + s.workdir,
	}, args...)
	cmd := exec.Command("git", full...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
