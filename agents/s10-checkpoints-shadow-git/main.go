package main

// main.go — a runnable demo of cline's shadow-git checkpoints.
//
// It plays out the lifecycle the agent loop drives every turn:
//
//	1. set up a workspace + a SEPARATE shadow git dir
//	2. write some files            -> Commit  (checkpoint #1)
//	3. edit + add more files       -> Commit  (checkpoint #2)
//	4. Restore checkpoint #1       -> the later edits vanish from disk
//	5. show the real .git is untouched, then continue the chain
//
// Everything runs in a throwaway temp directory so it pollutes nothing.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if _, err := lookGit(); err != nil {
		fmt.Println("git is not installed; checkpoints need a real git binary.")
		fmt.Println("(install git and re-run; the tests t.Skip when git is absent)")
		return
	}

	// A throwaway "workspace" standing in for the user's project, plus a shadow
	// git dir kept OUTSIDE the workspace so it can never be mistaken for the
	// user's real .git.
	base, err := os.MkdirTemp("", "s10-demo-*")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(base)

	workdir := filepath.Join(base, "workspace")
	shadowDir := filepath.Join(base, "shadow-git") // NOT workdir/.git
	must(os.MkdirAll(workdir, 0o755))

	fmt.Println("== shadow git checkpoints demo ==")
	fmt.Printf("workspace : %s\n", workdir)
	fmt.Printf("shadow dir: %s   (separate GIT_DIR — never the user's .git)\n\n", shadowDir)

	sg := &ShadowGit{}
	must(sg.Init(workdir, shadowDir))
	fmt.Println("[init] shadow repo created with an initial commit")

	var timeline []Checkpoint

	// ---- turn 1: the agent writes app.go ----------------------------------
	writeFile(workdir, "app.go", "package main\n\nfunc main() {}\n")
	c1 := snapshot(sg, &timeline, 1, "turn 1: add app.go")
	fmt.Printf("[turn 1] wrote app.go              -> checkpoint %s\n", short(c1))

	// ---- turn 2: the agent edits app.go and adds util.go ------------------
	writeFile(workdir, "app.go", "package main\n\nfunc main() { greet() }\n")
	writeFile(workdir, "util.go", "package main\n\nfunc greet() { println(\"hi\") }\n")
	c2 := snapshot(sg, &timeline, 2, "turn 2: edit app.go, add util.go")
	fmt.Printf("[turn 2] edited app.go + util.go   -> checkpoint %s\n", short(c2))

	fmt.Println("\nworkspace now:")
	listDir(workdir)

	// ---- the human hits undo: restore checkpoint #1 -----------------------
	fmt.Printf("\n[restore] resetting workspace to checkpoint %s (end of turn 1)\n", short(c1))
	must(sg.Restore(c1))

	fmt.Println("workspace after restore:")
	listDir(workdir) // util.go is gone; app.go is back to its turn-1 contents
	fmt.Printf("app.go contents: %q\n", readFile(workdir, "app.go"))

	// ---- the user's real repo is untouched --------------------------------
	if _, err := os.Stat(filepath.Join(workdir, ".git")); os.IsNotExist(err) {
		fmt.Println("\n[safety] workspace/.git does NOT exist — the user's history was never touched")
	}

	// ---- restore did not break the chain: keep committing -----------------
	writeFile(workdir, "app.go", "package main\n\nfunc main() { /* new path */ }\n")
	c3 := snapshot(sg, &timeline, 3, "turn 3: continue after restore")
	fmt.Printf("[turn 3] new edit after restore    -> checkpoint %s\n", short(c3))

	hashes, err := sg.List()
	must(err)
	fmt.Printf("\nshadow timeline now holds %d commits (newest first):\n", len(hashes))
	for _, h := range hashes {
		fmt.Printf("  %s\n", short(h))
	}
}

// snapshot commits the workspace and appends a Checkpoint to the timeline,
// exactly as the Task does after each turn.
func snapshot(sg *ShadowGit, timeline *[]Checkpoint, turn int, msg string) string {
	hash, err := sg.Commit(msg)
	must(err)
	*timeline = append(*timeline, Checkpoint{
		Hash:      hash,
		TurnIndex: turn,
		Message:   msg,
		CreatedAt: time.Now().Unix(),
	})
	return hash
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func writeFile(dir, name, content string) {
	must(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func readFile(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func listDir(dir string) {
	entries, err := os.ReadDir(dir)
	must(err)
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		fmt.Printf("  - %s\n", e.Name())
	}
}

// lookGit reports whether a git binary is on PATH.
func lookGit() (string, error) { return exec.LookPath("git") }

func must(err error) {
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
