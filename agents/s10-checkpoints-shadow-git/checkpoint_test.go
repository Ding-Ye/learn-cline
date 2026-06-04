package main

// checkpoint_test.go — exercises the shadow git against a REAL git binary in a
// throwaway t.TempDir workspace. Every test skips cleanly when git is absent
// (CI without git), so the suite never fails for an unrelated reason.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireGit skips the test when no git binary is on PATH. cline's checkpoints
// hard-depend on git; upstream throws "Git must be installed to use checkpoints"
// — we degrade to a skip instead so the rest of the suite still runs.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found on PATH; skipping shadow-git tests")
	}
}

// newShadow builds a fresh workspace + an out-of-tree shadow dir under t.TempDir
// and initializes the shadow repo. Returns the tracker and the workspace path.
func newShadow(t *testing.T) (*ShadowGit, string) {
	t.Helper()
	base := t.TempDir()
	workdir := filepath.Join(base, "workspace")
	shadowDir := filepath.Join(base, "shadow-git") // deliberately NOT workdir/.git
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	sg := &ShadowGit{}
	if err := sg.Init(workdir, shadowDir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return sg, workdir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// 1. Init creates a working shadow repo at the separate git dir, makes an
//    initial commit, and never creates a .git inside the user's workspace.
func TestInitCreatesShadowAndLeavesUserRepoUntouched(t *testing.T) {
	requireGit(t)
	sg, workdir := newShadow(t)

	// The shadow git dir must exist where we put it...
	if _, err := os.Stat(sg.shadowDir); err != nil {
		t.Fatalf("expected shadow git dir at %s: %v", sg.shadowDir, err)
	}
	// ...and there must be NO .git inside the workspace (the whole point).
	if _, err := os.Stat(filepath.Join(workdir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace/.git should not exist; the user's repo must be untouched")
	}
	// The initial commit means List() already returns exactly one hash.
	hashes, err := sg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("expected 1 initial commit, got %d", len(hashes))
	}
}

// 2. Commit after an edit returns a non-empty hash.
func TestCommitReturnsHash(t *testing.T) {
	requireGit(t)
	sg, workdir := newShadow(t)

	write(t, workdir, "main.go", "package main\n")
	hash, err := sg.Commit("turn 1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(hash) < 7 {
		t.Fatalf("expected a real commit hash, got %q", hash)
	}
}

// 3. Two checkpoints around a change produce DISTINCT hashes (the snapshot
//    actually captured the file change).
func TestSecondCommitAfterChangeIsDistinct(t *testing.T) {
	requireGit(t)
	sg, workdir := newShadow(t)

	write(t, workdir, "f.txt", "v1")
	h1, err := sg.Commit("c1")
	if err != nil {
		t.Fatalf("Commit c1: %v", err)
	}

	write(t, workdir, "f.txt", "v2")
	h2, err := sg.Commit("c2")
	if err != nil {
		t.Fatalf("Commit c2: %v", err)
	}

	if h1 == h2 {
		t.Fatalf("expected distinct hashes after a change, both were %q", h1)
	}
}

// 4. Restore rewinds file contents on disk to an earlier checkpoint — the undo.
func TestRestoreRevertsFileContents(t *testing.T) {
	requireGit(t)
	sg, workdir := newShadow(t)

	write(t, workdir, "app.go", "original\n")
	h1, err := sg.Commit("checkpoint 1")
	if err != nil {
		t.Fatalf("Commit 1: %v", err)
	}

	// Edit the file and add a new one, then snapshot.
	write(t, workdir, "app.go", "modified\n")
	write(t, workdir, "extra.go", "added\n")
	if _, err := sg.Commit("checkpoint 2"); err != nil {
		t.Fatalf("Commit 2: %v", err)
	}

	// Undo back to checkpoint 1.
	if err := sg.Restore(h1); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := read(t, workdir, "app.go"); got != "original\n" {
		t.Fatalf("app.go after restore = %q, want %q", got, "original\n")
	}
	// The file added after checkpoint 1 must be gone from the work-tree.
	if _, err := os.Stat(filepath.Join(workdir, "extra.go")); !os.IsNotExist(err) {
		t.Fatalf("extra.go should have been removed by restore")
	}
}

// 5. List reflects the commits made (initial + each Commit), newest first.
func TestListShowsCommits(t *testing.T) {
	requireGit(t)
	sg, workdir := newShadow(t)

	write(t, workdir, "a", "1")
	if _, err := sg.Commit("c1"); err != nil {
		t.Fatalf("Commit c1: %v", err)
	}
	write(t, workdir, "b", "2")
	if _, err := sg.Commit("c2"); err != nil {
		t.Fatalf("Commit c2: %v", err)
	}

	hashes, err := sg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// initial commit + c1 + c2 == 3
	if len(hashes) != 3 {
		t.Fatalf("expected 3 commits, got %d: %v", len(hashes), hashes)
	}
	for i, h := range hashes {
		if len(h) < 7 {
			t.Fatalf("hash %d looks invalid: %q", i, h)
		}
	}
}

// 6. Restoring then committing again continues the chain (HEAD advances from
//    the restored point). This is the "undo, then keep working" path.
func TestRestoreThenCommitContinuesChain(t *testing.T) {
	requireGit(t)
	sg, workdir := newShadow(t)

	write(t, workdir, "x", "first")
	h1, err := sg.Commit("c1")
	if err != nil {
		t.Fatalf("Commit c1: %v", err)
	}
	write(t, workdir, "x", "second")
	if _, err := sg.Commit("c2"); err != nil {
		t.Fatalf("Commit c2: %v", err)
	}

	if err := sg.Restore(h1); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Continue the chain after the restore.
	write(t, workdir, "x", "third")
	h3, err := sg.Commit("c3")
	if err != nil {
		t.Fatalf("Commit c3 after restore: %v", err)
	}
	if h3 == h1 {
		t.Fatalf("commit after restore should be a new hash, got the restored one")
	}
	if got := read(t, workdir, "x"); got != "third" {
		t.Fatalf("x = %q after continuing chain, want %q", got, "third")
	}
}
