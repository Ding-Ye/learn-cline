package main

import (
	"flag"
	"fmt"
	"os"
)

// main is an offline demo of MCP tool integration. No subprocess, no network:
// it connects an McpClient to an in-process FakeServer, runs the
// initialize → tools/list → tools/call sequence, merges the discovered tools
// into the same Registry a local agent would use, and dispatches a couple of
// calls through it.
//
//	go run .       # connect, discover, merge, call
//	go run . -v    # also print the raw initialize handshake result
func main() {
	verbose := flag.Bool("v", false, "print the initialize handshake result")
	flag.Parse()

	// 1. Stand up the fake server and a client wired to it. In production the
	//    transport would be a stdio subprocess (cline: StdioClientTransport);
	//    here the server IS the transport, so this is fully hermetic.
	server := NewFakeServer()
	client := NewMcpClient(server)

	// 2. Handshake. cline does this inside connectToServer before touching tools.
	info, err := client.Initialize()
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialize failed:", err)
		os.Exit(1)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "[s09] handshake ok: server=%s v%s protocol=%s\n\n",
			info.ServerInfo.Name, info.ServerInfo.Version, info.ProtocolVersion)
	}

	// 3. Discover the server's tools and merge them into the registry. After this
	//    the remote tools are indistinguishable from compiled-in ones — that is
	//    the point of the chapter.
	schemas, err := client.ListTools()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tools/list failed:", err)
		os.Exit(1)
	}

	reg := NewRegistry()
	merged := make([]Tool, 0, len(schemas))
	for _, s := range schemas {
		merged = append(merged, &mcpTool{client: client, schema: s})
	}
	reg.Merge(merged)

	fmt.Printf("discovered %d MCP tool(s):\n", len(reg.Schemas()))
	for _, s := range reg.Schemas() {
		fmt.Printf("  - %-6s %s\n", s.Name, s.Description)
	}

	// 4. Dispatch calls THROUGH the registry, exactly as the agent loop would for
	//    any tool. The registry doesn't know these are remote — Execute routes to
	//    tools/call under the hood.
	fmt.Println("\ncalling tools through the registry:")
	runDemoCall(reg, "echo", map[string]interface{}{"text": "hello from MCP"})
	runDemoCall(reg, "add", map[string]interface{}{"a": 2, "b": 40})
	// A tool-level failure surfaces as an error result, not a crash.
	runDemoCall(reg, "echo", map[string]interface{}{"text": ""})
}

// runDemoCall looks a tool up in the registry and invokes it, printing a one-line
// ok/ERROR summary — the same dispatch shape s03 used, now backed by an MCP server.
func runDemoCall(reg *Registry, name string, args map[string]interface{}) {
	tool, ok := reg.Get(name)
	if !ok {
		fmt.Printf("  %-6s -> ERROR unknown tool\n", name)
		return
	}
	out, err := tool.Execute(args)
	if err != nil {
		fmt.Printf("  %-6s -> ERROR %s\n", name, err)
		return
	}
	fmt.Printf("  %-6s -> ok    %s\n", name, out)
}
