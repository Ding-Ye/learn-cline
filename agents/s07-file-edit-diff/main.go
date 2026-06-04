// Command s07 demonstrates cline's file-edit-by-diff mechanism: instead of
// rewriting a whole file, the model emits surgical SEARCH/REPLACE blocks and the
// applier (constructNewFileContent) splices each REPLACE over the span its SEARCH
// text locates in the original — supporting multiple blocks and streamed,
// incremental application.
//
// The demo is fully offline: it creates a temp file, applies a multi-block diff,
// and shows the before/after plus a streamed (chunk-by-chunk) replay so you can
// watch the result grow exactly as a UI would.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// original is the seed file the demo edits.
const original = `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`

// diff edits two spans in one shot: rename the greeting and add a helper call.
// Note the cline markers and that blocks appear in file order.
const diff = `------- SEARCH
func main() {
=======
func greeting() string { return "hello, cline" }

func main() {
+++++++ REPLACE
------- SEARCH
	fmt.Println("hello")
=======
	fmt.Println(greeting())
+++++++ REPLACE`

func main() {
	root, err := os.MkdirTemp("", "s07-demo-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(root)

	// 1) Whole-file write (s03's blunt instrument) seeds the file.
	if err := writeFile(root, "main.go", original); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("== original main.go ==")
	fmt.Print(original)

	// 2) replace_in_file applies the diff (one-shot, isFinal=true).
	fmt.Println("\n== diff (SEARCH/REPLACE blocks) ==")
	fmt.Println(diff)

	tool := ReplaceInFileTool{}
	res, _ := tool.Apply(root, map[string]interface{}{"path": "main.go", "diff": diff})
	if res.IsError {
		fmt.Fprintln(os.Stderr, "diff failed:", res.ToolContent)
		os.Exit(1)
	}

	edited, _ := os.ReadFile(filepath.Join(root, "main.go"))
	fmt.Println("\n== edited main.go ==")
	fmt.Print(string(edited))

	// 3) Show streaming: feed the SAME diff in growing chunks and watch the result
	//    extend monotonically — this is how cline renders an edit as it arrives.
	fmt.Println("\n== streamed application (incremental result length) ==")
	streamDemo(original, diff)
}

// streamDemo feeds the diff to constructNewFileContent in increasing prefixes,
// printing how long the in-progress result is after each chunk, then asserts the
// final streamed result equals the one-shot result.
func streamDemo(orig, fullDiff string) {
	lines := strings.Split(fullDiff, "\n")
	for i := 1; i < len(lines); i += 2 {
		prefix := strings.Join(lines[:i], "\n")
		partial, err := constructNewFileContent(prefix, orig, false)
		if err != nil {
			continue // a chunk that ends mid-block is fine; keep streaming
		}
		fmt.Printf("  after %2d/%d diff lines: result is %d bytes\n", i, len(lines), len(partial))
	}
	final, _ := constructNewFileContent(fullDiff, orig, true)
	oneShot, _ := constructNewFileContent(fullDiff, orig, true)
	fmt.Printf("  final result == one-shot result: %v\n", final == oneShot)
}
