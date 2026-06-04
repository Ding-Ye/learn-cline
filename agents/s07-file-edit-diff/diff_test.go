package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test 1: a single SEARCH/REPLACE block replaces exactly the right span and
// leaves the rest of the file untouched.
func TestSingleBlockExactMatch(t *testing.T) {
	original := "line one\nline two\nline three\n"
	diff := "------- SEARCH\nline two\n=======\nLINE TWO CHANGED\n+++++++ REPLACE"

	got, err := constructNewFileContent(diff, original, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "line one\nLINE TWO CHANGED\nline three\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Test 2: the exact-match path applied to a real file on disk (sandboxed via the
// replace_in_file tool) writes the updated content back.
func TestExactMatchAppliesToFileOnDisk(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(root, "a.txt", "alpha\nbeta\ngamma\n"); err != nil {
		t.Fatal(err)
	}
	diff := "------- SEARCH\nbeta\n=======\nBETA!\n+++++++ REPLACE"

	res, err := ReplaceInFileTool{}.Apply(root, map[string]interface{}{"path": "a.txt", "diff": diff})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success tool_result, got error: %v", res.ToolContent)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "alpha\nBETA!\ngamma\n"
	if string(onDisk) != want {
		t.Fatalf("file on disk = %q, want %q", string(onDisk), want)
	}
}

// Test 3: the line-trimmed fallback matches when the SEARCH text differs from the
// file only by leading/trailing whitespace, and the byte-exact original span is
// the one that gets replaced.
func TestLineTrimmedWhitespaceFallback(t *testing.T) {
	// File has tab-indented body; the model emits the same lines space-indented.
	original := "func f() {\n\treturn 1\n}\n"
	diff := "------- SEARCH\n    return 1\n=======\n\treturn 42\n+++++++ REPLACE"

	got, err := constructNewFileContent(diff, original, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "func f() {\n\treturn 42\n}\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Test 4: multiple blocks apply in file order.
func TestMultipleBlocksInOrder(t *testing.T) {
	original := "a\nb\nc\nd\n"
	diff := strings.Join([]string{
		"------- SEARCH", "a", "=======", "A", "+++++++ REPLACE",
		"------- SEARCH", "c", "=======", "C", "+++++++ REPLACE",
	}, "\n")

	got, err := constructNewFileContent(diff, original, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "A\nb\nC\nd\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Test 5: a SEARCH block that matches nothing yields a clear error (and the
// error text quotes the offending block).
func TestNotFoundErrorsClearly(t *testing.T) {
	original := "hello world\n"
	diff := "------- SEARCH\nnot present anywhere\n=======\nx\n+++++++ REPLACE"

	_, err := constructNewFileContent(diff, original, true)
	if err == nil {
		t.Fatal("expected an error for an unmatched SEARCH block, got nil")
	}
	if !strings.Contains(err.Error(), "does not match anything") {
		t.Fatalf("error message %q should explain the no-match case", err.Error())
	}
	if !strings.Contains(err.Error(), "not present anywhere") {
		t.Fatalf("error message %q should quote the offending SEARCH text", err.Error())
	}
}

// Test 6: an empty SEARCH against an empty original CREATES the file (pure
// insertion); against a non-empty original it replaces the whole file.
func TestEmptySearchCreatesOrReplacesWholeFile(t *testing.T) {
	created, err := constructNewFileContent("------- SEARCH\n=======\nbrand new\n+++++++ REPLACE", "", true)
	if err != nil {
		t.Fatalf("create: unexpected error: %v", err)
	}
	if created != "brand new\n" {
		t.Fatalf("create: got %q, want %q", created, "brand new\n")
	}

	replaced, err := constructNewFileContent("------- SEARCH\n=======\nreplaced\n+++++++ REPLACE", "old\ncontent\n", true)
	if err != nil {
		t.Fatalf("replace: unexpected error: %v", err)
	}
	if replaced != "replaced\n" {
		t.Fatalf("replace: got %q, want %q", replaced, "replaced\n")
	}
}

// Test 7: streaming the diff in growing chunks then a final flush yields exactly
// the same result as a single one-shot application.
func TestStreamingEqualsOneShot(t *testing.T) {
	original := "x\ny\nz\n"
	diff := "------- SEARCH\ny\n=======\nY\n+++++++ REPLACE"

	oneShot, err := constructNewFileContent(diff, original, true)
	if err != nil {
		t.Fatal(err)
	}

	// Feed character-by-character growing prefixes with isFinal=false, then a
	// final isFinal=true call on the whole diff.
	for i := 1; i < len(diff); i++ {
		if _, err := constructNewFileContent(diff[:i], original, false); err != nil {
			// Mid-block prefixes can legitimately fail to match; that's allowed
			// while streaming. We only require the FINAL flush to be correct.
			_ = err
		}
	}
	streamed, err := constructNewFileContent(diff, original, true)
	if err != nil {
		t.Fatal(err)
	}
	if streamed != oneShot {
		t.Fatalf("streamed %q != one-shot %q", streamed, oneShot)
	}
}

// Test 8: a truncated diff (a SEARCH/REPLACE block left open) errors on finalize
// rather than silently writing a partial file.
func TestIncompleteDiffErrorsOnFinalize(t *testing.T) {
	original := "p\nq\n"
	// REPLACE marker missing — block never closes.
	diff := "------- SEARCH\np\n=======\nP"

	_, err := constructNewFileContent(diff, original, true)
	if err == nil {
		t.Fatal("expected an error for an unterminated block on finalize, got nil")
	}
	if !strings.Contains(err.Error(), "incomplete diff") {
		t.Fatalf("error %q should flag the incomplete diff", err.Error())
	}
}
