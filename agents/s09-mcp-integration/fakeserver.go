package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// A FAKE in-process MCP server.
//
// In production an MCP server is a separate process the client talks to over
// stdio. For teaching and for hermetic tests we don't want to spawn anything:
// FakeServer implements the SAME JSON-RPC 2.0 protocol entirely in-process and
// satisfies the Transport interface directly (RoundTrip decodes the request,
// dispatches by method, and encodes a response). Because the client only sees a
// Transport, it cannot tell this from a real subprocess — which is the whole
// argument for putting the transport behind an interface.
//
// It implements the three methods the client uses: initialize, tools/list,
// tools/call. It also returns proper JSON-RPC errors for unknown methods
// (-32601) and malformed requests (-32700), so the error paths are testable.
// ---------------------------------------------------------------------------

// fakeTool is a tool the fake server exposes: its advertised definition plus a
// pure Go handler that produces the result. Real servers run arbitrary code
// here (query a DB, hit an API); ours just need to prove the round-trip.
type fakeTool struct {
	def     mcpToolDef
	handler func(args map[string]interface{}) (string, error)
}

// FakeServer is an in-process MCP server. tools is keyed by tool name.
type FakeServer struct {
	tools map[string]fakeTool
}

// NewFakeServer builds a server pre-loaded with two demo tools:
//
//	echo    — returns its "text" argument verbatim.
//	add     — returns the sum of integer args "a" and "b".
//
// Two tools (rather than one) let tests prove that tools/list returns a SET and
// that tools/call dispatches by name to the right handler.
func NewFakeServer() *FakeServer {
	s := &FakeServer{tools: make(map[string]fakeTool)}

	s.tools["echo"] = fakeTool{
		def: mcpToolDef{
			Name:        "echo",
			Description: "Echo back the provided text.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"text"},
			},
		},
		handler: func(args map[string]interface{}) (string, error) {
			text, _ := args["text"].(string)
			if strings.TrimSpace(text) == "" {
				// A tool-level failure: reported via isError in the RESULT, not as
				// a JSON-RPC error. The model gets to see it and recover.
				return "", fmt.Errorf("echo requires a non-empty 'text' argument")
			}
			return text, nil
		},
	}

	s.tools["add"] = fakeTool{
		def: mcpToolDef{
			Name:        "add",
			Description: "Add two integers a and b.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"a": map[string]interface{}{"type": "integer"},
					"b": map[string]interface{}{"type": "integer"},
				},
				"required": []interface{}{"a", "b"},
			},
		},
		handler: func(args map[string]interface{}) (string, error) {
			a, b := toInt(args["a"]), toInt(args["b"])
			return fmt.Sprintf("%d", a+b), nil
		},
	}

	return s
}

// toInt coerces a JSON number (which decodes to float64) into an int. JSON has
// no integer type, so this normalization is unavoidable when reading arguments.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// RoundTrip makes FakeServer a Transport: it takes the raw request bytes the
// client wrote, dispatches by JSON-RPC method, and returns the raw response
// bytes. This is the in-process stand-in for "write to the child's stdin, read
// its stdout". Splitting decode → handle → encode here is what lets the
// malformed-request case (-32700) and unknown-method case (-32601) be exercised.
func (s *FakeServer) RoundTrip(reqBytes []byte) ([]byte, error) {
	var req Request
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		// Malformed JSON: a real server replies with a parse-error object rather
		// than dropping the connection. id is unknown, so it stays zero.
		return encodeError(0, codeParseError, "parse error: "+err.Error())
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return encodeError(req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

// handleInitialize echoes the protocol version and announces server identity —
// the minimal honest handshake.
func (s *FakeServer) handleInitialize(req Request) ([]byte, error) {
	return encodeResult(req.ID, InitializeResult{
		ProtocolVersion: mcpProtocolVersion,
		ServerInfo:      serverInfo{Name: "fake-mcp-server", Version: "1.0.0"},
	})
}

// handleToolsList returns every tool's definition, name-sorted for determinism.
func (s *FakeServer) handleToolsList(req Request) ([]byte, error) {
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	sortStrings(names)
	defs := make([]mcpToolDef, 0, len(names))
	for _, name := range names {
		defs = append(defs, s.tools[name].def)
	}
	return encodeResult(req.ID, listToolsResult{Tools: defs})
}

// handleToolsCall dispatches to the named tool's handler. An unknown tool name
// and a handler error both come back as a RESULT with isError:true (a tool-level
// failure), distinct from a JSON-RPC protocol error.
func (s *FakeServer) handleToolsCall(req Request) ([]byte, error) {
	var p callToolParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return encodeError(req.ID, codeParseError, "bad tools/call params: "+err.Error())
	}
	tool, ok := s.tools[p.Name]
	if !ok {
		return encodeResult(req.ID, errorResult("unknown tool: "+p.Name))
	}
	out, err := tool.handler(p.Arguments)
	if err != nil {
		return encodeResult(req.ID, errorResult(err.Error()))
	}
	return encodeResult(req.ID, CallToolResult{
		Content: []mcpContent{{Type: "text", Text: out}},
	})
}

// errorResult builds a tool-level failure result (isError:true) carrying the
// message as text content.
func errorResult(msg string) CallToolResult {
	return CallToolResult{
		Content: []mcpContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// encodeResult marshals a successful JSON-RPC response.
func encodeResult(id int, result interface{}) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Response{JSONRPC: jsonRPCVersion, ID: id, Result: raw})
}

// encodeError marshals a JSON-RPC error response.
func encodeError(id, code int, msg string) ([]byte, error) {
	return json.Marshal(Response{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	})
}
