package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// newTestClient wires a client to a fresh in-process fake server. No subprocess,
// so every test is hermetic.
func newTestClient(t *testing.T) *McpClient {
	t.Helper()
	return NewMcpClient(NewFakeServer())
}

// 1. The initialize handshake succeeds and negotiates a protocol version.
func TestInitializeHandshake(t *testing.T) {
	c := newTestClient(t)
	res, err := c.Initialize()
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if res.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("protocol version = %q, want %q", res.ProtocolVersion, mcpProtocolVersion)
	}
	if res.ServerInfo.Name != "fake-mcp-server" {
		t.Errorf("server name = %q, want %q", res.ServerInfo.Name, "fake-mcp-server")
	}
	if !c.initialized {
		t.Error("client should be marked initialized after a successful handshake")
	}
}

// ListTools / CallTool must refuse to run before the handshake.
func TestToolsRequireInitialize(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.ListTools(); err == nil {
		t.Error("ListTools before Initialize should error")
	}
	if _, err := c.CallTool("echo", map[string]interface{}{"text": "x"}); err == nil {
		t.Error("CallTool before Initialize should error")
	}
}

// 2. tools/list maps each remote tool definition to our canonical ToolSchema,
//    including the JSON-Schema input schema.
func TestListToolsMapsToToolSchema(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	schemas, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(schemas) != 2 {
		t.Fatalf("got %d schemas, want 2", len(schemas))
	}
	// Name-sorted by the server: add, echo.
	if schemas[0].Name != "add" || schemas[1].Name != "echo" {
		t.Fatalf("schema names = %q, %q; want add, echo", schemas[0].Name, schemas[1].Name)
	}
	echo := schemas[1]
	if echo.Description == "" {
		t.Error("echo schema lost its description in conversion")
	}
	if echo.InputSchema["type"] != "object" {
		t.Errorf("echo input schema type = %v, want object", echo.InputSchema["type"])
	}
}

// 3. tools/call round-trips arguments and returns the tool's output.
func TestCallToolReturnsResult(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	echo, err := c.CallTool("echo", map[string]interface{}{"text": "round-trip"})
	if err != nil {
		t.Fatalf("CallTool echo: %v", err)
	}
	if echo.IsError {
		t.Errorf("echo unexpectedly errored: %s", echo.Text())
	}
	if echo.Text() != "round-trip" {
		t.Errorf("echo result = %q, want %q", echo.Text(), "round-trip")
	}

	// A second tool by name, to prove dispatch isn't hard-wired to one handler.
	add, err := c.CallTool("add", map[string]interface{}{"a": 2, "b": 40})
	if err != nil {
		t.Fatalf("CallTool add: %v", err)
	}
	if add.Text() != "42" {
		t.Errorf("add result = %q, want %q", add.Text(), "42")
	}
}

// A tool-level failure (bad args) comes back as a RESULT with isError:true, not
// as a transport/JSON-RPC error — so the model can read it and recover.
func TestCallToolToolLevelError(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	res, err := c.CallTool("echo", map[string]interface{}{"text": ""})
	if err != nil {
		t.Fatalf("CallTool should not return a transport error for a tool-level failure: %v", err)
	}
	if !res.IsError {
		t.Error("empty-text echo should set IsError on the result")
	}
	if !strings.Contains(res.Text(), "non-empty") {
		t.Errorf("error text = %q, want it to mention the failure", res.Text())
	}

	// Unknown tool name is also a tool-level error result.
	unknown, err := c.CallTool("does_not_exist", nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !unknown.IsError || !strings.Contains(unknown.Text(), "unknown tool") {
		t.Errorf("unknown tool result = %+v, want IsError with 'unknown tool'", unknown)
	}
}

// 4. An unknown JSON-RPC method yields a -32601 protocol error.
func TestUnknownMethodError(t *testing.T) {
	c := newTestClient(t)
	err := c.call("totally/bogus", struct{}{}, nil)
	if err == nil {
		t.Fatal("unknown method should error")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error = %v (%T), want *RPCError", err, err)
	}
	if rpcErr.Code != codeMethodNotFound {
		t.Errorf("error code = %d, want %d (method not found)", rpcErr.Code, codeMethodNotFound)
	}
}

// 5. A malformed request yields a -32700 parse error from the server (and the
//    transport seam is what makes this reachable: the server decodes raw bytes).
func TestMalformedRequestError(t *testing.T) {
	server := NewFakeServer()
	respBytes, err := server.RoundTrip([]byte(`{"jsonrpc":"2.0","id":1,"method":`)) // truncated JSON
	if err != nil {
		t.Fatalf("RoundTrip should return a JSON-RPC error response, not a Go error: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("response itself should be valid JSON: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("malformed request should produce an error response")
	}
	if resp.Error.Code != codeParseError {
		t.Errorf("error code = %d, want %d (parse error)", resp.Error.Code, codeParseError)
	}
}

// 6. End-to-end: DiscoverTools folds the server's tools into a Registry, and a
//    discovered tool dispatches like any local one — the structural payoff of s09.
func TestDiscoveredToolsMergeIntoRegistry(t *testing.T) {
	client := newTestClient(t)
	tools, err := DiscoverTools(client)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}

	reg := NewRegistry()
	reg.Merge(tools)

	if len(reg.Schemas()) != 2 {
		t.Fatalf("registry has %d tools after merge, want 2", len(reg.Schemas()))
	}

	// Dispatch through the registry, not the client — proving the remote tool is
	// a first-class Tool indistinguishable from a compiled-in one.
	tool, ok := reg.Get("add")
	if !ok {
		t.Fatal("merged 'add' tool not found in registry")
	}
	out, err := tool.Execute(map[string]interface{}{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("registry dispatch of MCP tool: %v", err)
	}
	if out != "3" {
		t.Errorf("add via registry = %q, want %q", out, "3")
	}

	// The mcpTool adapter must turn a tool-level failure into a Go error so the
	// executor wraps it as an error tool_result (the s03 discipline).
	echo, _ := reg.Get("echo")
	if _, err := echo.Execute(map[string]interface{}{"text": ""}); err == nil {
		t.Error("mcpTool.Execute should return an error when the result IsError")
	}
}
