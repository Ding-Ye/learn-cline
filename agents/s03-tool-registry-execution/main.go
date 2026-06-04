package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

// main is an offline demo of the tool registry + executor. No network, no API
// key. It builds a sandbox dir, registers four real tools, then dispatches a
// scripted sequence of parsed tool_use blocks — exactly the kind s02's parser
// emits and s01's loop would feed in — and prints the tool_result for each.
//
//	go run .          # the scripted sequence
//	go run . -v       # also print each tool's registered schema
func main() {
	verbose := flag.Bool("v", false, "print registered tool schemas before running")
	flag.Parse()

	// A throwaway sandbox so the demo never touches anything real.
	base, err := os.MkdirTemp("", "s03-demo-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdir temp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(base)

	// 1. Build the registry and register four handlers by name. This is the
	//    Go analogue of cline's registerToolHandlers() populating the coordinator.
	reg := NewRegistry()
	reg.Register(&WriteToFileTool{Base: base})
	reg.Register(&ReadFileTool{Base: base})
	reg.Register(&ListFilesTool{Base: base})
	reg.Register(&ExecuteCommandTool{Base: base})

	exec := NewToolExecutor(reg)

	if *verbose {
		fmt.Fprintln(os.Stderr, "[s03] registered tools:")
		for _, s := range reg.Schemas() {
			fmt.Fprintf(os.Stderr, "  - %-16s %s\n", s.Name, s.Description)
		}
		fmt.Fprintln(os.Stderr, "")
	}

	// 2. A scripted run: write a file, read it back, list the dir, run a command,
	//    then two failure cases (unknown tool, missing param) to show the executor
	//    returns an error RESULT instead of crashing.
	blocks := []ToolUse{
		{Name: "write_to_file", CallID: "t1", Params: map[string]string{"path": "notes/hello.txt", "content": "hi from s03"}},
		{Name: "read_file", CallID: "t2", Params: map[string]string{"path": "notes/hello.txt"}},
		{Name: "list_files", CallID: "t3", Params: map[string]string{"path": "notes"}},
		{Name: "execute_command", CallID: "t4", Params: map[string]string{"command": "echo registry-and-dispatch"}},
		{Name: "fly_to_moon", CallID: "t5", Params: map[string]string{"destination": "moon"}},
		{Name: "read_file", CallID: "t6", Params: map[string]string{}}, // missing required "path"
	}

	ctx := context.Background()
	for _, b := range blocks {
		res := exec.Execute(ctx, b)
		status := "ok"
		if res.IsError {
			status = "ERROR"
		}
		fmt.Printf("[%s] %-16s -> %-5s %s\n", b.CallID, b.Name, status, firstLine(res.Output))
	}
}

// firstLine returns the first line of s, with a marker if it was truncated, so
// multi-line tool output (e.g. list_files) stays on one printed row.
func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i] + " …"
		}
	}
	return s
}
