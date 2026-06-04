package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test: the registry resolves a tool by name after registration
// (ToolExecutorCoordinator.register / has / getHandler).
func TestRegistryRegisterAndLookup(t *testing.T) {
	reg := NewRegistry()
	if reg.Has("read_file") {
		t.Fatal("empty registry should not have read_file")
	}
	reg.Register(&ReadFileTool{Base: t.TempDir()})

	if !reg.Has("read_file") {
		t.Fatal("registry should have read_file after Register")
	}
	h, ok := reg.Get("read_file")
	if !ok {
		t.Fatal("Get(read_file) returned ok=false")
	}
	if h.Schema().Name != "read_file" {
		t.Fatalf("resolved handler name = %q, want read_file", h.Schema().Name)
	}
}

// Test: the read_file handler returns file contents from the sandbox.
func TestReadFileHandler(t *testing.T) {
	base := t.TempDir()
	want := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{Base: base}
	got, err := tool.Execute(context.Background(), map[string]string{"path": "main.go"})
	if err != nil {
		t.Fatalf("read_file Execute: %v", err)
	}
	if got != want {
		t.Fatalf("read_file content = %q, want %q", got, want)
	}
}

// Test: the write_to_file handler writes to a t.TempDir-backed sandbox, creating
// parent directories, and the bytes land on disk.
func TestWriteToFileHandlerWritesToTempDir(t *testing.T) {
	base := t.TempDir()
	tool := &WriteToFileTool{Base: base}

	out, err := tool.Execute(context.Background(), map[string]string{
		"path":    "sub/dir/out.txt",
		"content": "hello s03",
	})
	if err != nil {
		t.Fatalf("write_to_file Execute: %v", err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("write_to_file output = %q, want it to mention bytes written", out)
	}

	onDisk, err := os.ReadFile(filepath.Join(base, "sub", "dir", "out.txt"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(onDisk) != "hello s03" {
		t.Fatalf("on-disk content = %q, want %q", string(onDisk), "hello s03")
	}
}

// Test: list_files returns sorted entries with a trailing slash on directories.
func TestListFilesHandler(t *testing.T) {
	base := t.TempDir()
	mustWrite(t, base, "b.txt", "b")
	mustWrite(t, base, "a.txt", "a")
	if err := os.Mkdir(filepath.Join(base, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := &ListFilesTool{Base: base}
	got, err := tool.Execute(context.Background(), map[string]string{"path": "."})
	if err != nil {
		t.Fatalf("list_files Execute: %v", err)
	}
	want := "a.txt\nb.txt\nzdir/"
	if got != want {
		t.Fatalf("list_files = %q, want %q", got, want)
	}
}

// Test: the sandbox rejects a path that escapes the base dir, so a tool can
// never read/write outside its workspace.
func TestSandboxRejectsEscape(t *testing.T) {
	base := t.TempDir()
	tool := &ReadFileTool{Base: base}
	_, err := tool.Execute(context.Background(), map[string]string{"path": "../../../etc/hosts"})
	if err == nil {
		t.Fatal("expected sandbox escape to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "escapes the sandbox") {
		t.Fatalf("error = %v, want a sandbox-escape error", err)
	}
}

func mustWrite(t *testing.T, base, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(base, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
