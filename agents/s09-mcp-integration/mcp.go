package main

import (
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// A minimal MCP (Model Context Protocol) client over JSON-RPC 2.0.
//
// MCP is how cline loads tools from EXTERNAL servers at runtime instead of
// compiling them all in. A server speaks JSON-RPC 2.0 (over stdio in the real
// world); the client performs three calls that matter for tools:
//
//	initialize  — handshake: agree on protocol version + exchange capabilities.
//	tools/list  — ask the server what tools it has.
//	tools/call  — invoke one tool with arguments, get its result back.
//
// cline's McpHub wraps the official SDK Client and does exactly this sequence:
// connectToServer (L286) opens a transport and connects, fetchToolsList (L677)
// issues "tools/list", and callTool (L1233) issues "tools/call". s09 reimplements
// the protocol directly so you can see the wire, and routes the transport
// through an interface so a FAKE in-process server can stand in for a subprocess
// (the real Transport) in tests — no external process, fully hermetic.
// ---------------------------------------------------------------------------

const jsonRPCVersion = "2.0"

// mcpProtocolVersion is the protocol date string the client offers in the
// initialize handshake. The real spec uses dated versions like this; the value
// is opaque to our teaching server, which simply echoes the negotiated one.
const mcpProtocolVersion = "2024-11-05"

// Request is a JSON-RPC 2.0 request. ID pairs a response to its request; Method
// is e.g. "tools/list"; Params is the method-specific payload (left as raw JSON
// so the transport layer is method-agnostic).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response. Exactly one of Result / Error is set.
// Result stays raw so the client decodes it into the shape each method expects.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object. Code follows the spec's reserved
// ranges: -32601 = "method not found", -32700 = "parse error".
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Standard JSON-RPC 2.0 error codes used by the fake server (and recognized by
// tests). They are part of the protocol, not invented here.
const (
	codeParseError    = -32700 // malformed JSON request
	codeMethodNotFound = -32601 // unknown method
)

// Transport carries one JSON-RPC request and returns its response. This is the
// seam that lets a real stdio subprocess and an in-process fake be swapped
// freely: the client never knows which it is talking to.
//
//   - Real life: marshal the request, write it to the child's stdin, read a line
//     from its stdout, unmarshal. (cline delegates this to the MCP SDK's
//     StdioClientTransport — apps/vscode/src/services/mcp/McpHub.ts L384.)
//   - Tests/demo: call the fake server's handler in-process (fakeserver.go).
//
// Returning the request as bytes (not a typed Request) keeps the transport
// honest about framing: a transport that mangles JSON will be caught, which is
// how the malformed-request test works.
type Transport interface {
	RoundTrip(req []byte) ([]byte, error)
}

// McpClient is a single connection to one MCP server. It owns the request-id
// counter and the transport; the three methods below are the entire tool surface
// the agent needs.
type McpClient struct {
	transport   Transport
	nextID      int
	initialized bool
}

// NewMcpClient wraps a transport. No I/O happens until Initialize.
func NewMcpClient(t Transport) *McpClient {
	return &McpClient{transport: t}
}

// call is the one place a JSON-RPC round-trip happens: assign an id, marshal,
// hand the bytes to the transport, unmarshal the reply, and surface a protocol
// error as a Go error. Every public method is a thin typed wrapper over this.
func (c *McpClient) call(method string, params interface{}, result interface{}) error {
	c.nextID++
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params for %s: %w", method, err)
		}
		raw = b
	}
	reqBytes, err := json.Marshal(Request{
		JSONRPC: jsonRPCVersion,
		ID:      c.nextID,
		Method:  method,
		Params:  raw,
	})
	if err != nil {
		return fmt.Errorf("marshal request %s: %w", method, err)
	}

	respBytes, err := c.transport.RoundTrip(reqBytes)
	if err != nil {
		return fmt.Errorf("transport %s: %w", method, err)
	}

	var resp Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("unmarshal response for %s: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error // a *RPCError; method-not-found, parse-error, etc.
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("unmarshal result for %s: %w", method, err)
		}
	}
	return nil
}

// --- initialize -------------------------------------------------------------

// initializeParams is what the client offers in the handshake: the protocol
// version it speaks and who it is. (The real handshake also exchanges detailed
// capability objects; we keep just the shape that proves the negotiation ran.)
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ClientInfo      clientInfo     `json:"clientInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the server's half of the handshake: the version it agreed
// to and its identity.
type InitializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ServerInfo      serverInfo `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Initialize performs the MCP handshake. It MUST be called before ListTools /
// CallTool — a server is entitled to reject tool traffic on an un-initialized
// connection, and a real one tracks lifecycle state. cline runs this inside
// client.connect() during connectToServer (McpHub.ts L286) before any
// fetchToolsList call.
func (c *McpClient) Initialize() (*InitializeResult, error) {
	var res InitializeResult
	err := c.call("initialize", initializeParams{
		ProtocolVersion: mcpProtocolVersion,
		ClientInfo:      clientInfo{Name: "learn-cline-s09", Version: "0.1.0"},
		Capabilities:    map[string]any{},
	}, &res)
	if err != nil {
		return nil, err
	}
	c.initialized = true
	return &res, nil
}

// --- tools/list -------------------------------------------------------------

// mcpToolDef is one tool as the server describes it. Note inputSchema is a
// JSON-Schema object — the same idea as our ToolSchema.InputSchema — which is why
// the conversion in ListTools is nearly a field copy.
type mcpToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type listToolsResult struct {
	Tools []mcpToolDef `json:"tools"`
}

// ListTools issues "tools/list" and converts each remote definition into our
// canonical ToolSchema. This is the crux of the chapter: after this call, the
// server's tools are described in exactly the vocabulary the registry and the
// model already speak. cline's fetchToolsList (McpHub.ts L677-706) is the same
// request followed by a small map step (it also tags each tool with an
// autoApprove flag from settings — omitted here; that's s04's concern).
func (c *McpClient) ListTools() ([]ToolSchema, error) {
	if !c.initialized {
		return nil, fmt.Errorf("ListTools: client not initialized (call Initialize first)")
	}
	var res listToolsResult
	if err := c.call("tools/list", struct{}{}, &res); err != nil {
		return nil, err
	}
	out := make([]ToolSchema, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

// --- tools/call -------------------------------------------------------------

// callToolParams names the tool and its arguments. This mirrors the params block
// in cline's callTool request (McpHub.ts L1271-1276): { name, arguments }.
type callToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// CallToolResult is the server's reply to tools/call. MCP returns content as a
// list of typed parts (text/image/...); we model the text part and an isError
// flag. cline returns the same { content, isError } shape (McpToolCallResponse,
// McpHub.ts L1293-1296) and renders it into a tool_result.
type CallToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"` // "text" (the only kind s09 handles)
	Text string `json:"text"`
}

// Text flattens the content parts into a single string — the form the agent
// loop wants when it builds a tool_result block.
func (r CallToolResult) Text() string {
	out := ""
	for i, c := range r.Content {
		if i > 0 {
			out += "\n"
		}
		out += c.Text
	}
	return out
}

// CallTool issues "tools/call" for one tool and returns the structured result.
// A tool that fails its own work signals it via IsError in the RESULT (not a
// JSON-RPC error) so the model can read what went wrong — exactly cline's
// distinction between a transport/protocol failure (thrown) and a tool-level
// failure (isError:true in the response). callTool in McpHub.ts (L1269-1296)
// passes { name, arguments } through and hands back { ...result, content }.
func (c *McpClient) CallTool(name string, args map[string]interface{}) (*CallToolResult, error) {
	if !c.initialized {
		return nil, fmt.Errorf("CallTool: client not initialized (call Initialize first)")
	}
	if args == nil {
		args = map[string]interface{}{} // the spec wants an object, never null
	}
	var res CallToolResult
	if err := c.call("tools/call", callToolParams{Name: name, Arguments: args}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// --- remote tool adapter ----------------------------------------------------

// mcpTool adapts one discovered MCP tool into the registry's Tool interface, so
// a server-provided tool is dispatched the same way as a compiled-in one. This
// is the adapter that makes discovered tools first-class: Schema() returns the
// schema we got from tools/list, and Execute() routes to tools/call. In cline
// the equivalent indirection is UseMcpToolHandler routing through McpHub.callTool.
type mcpTool struct {
	client *McpClient
	schema ToolSchema
}

func (t *mcpTool) Schema() ToolSchema { return t.schema }

func (t *mcpTool) Execute(args map[string]interface{}) (string, error) {
	res, err := t.client.CallTool(t.schema.Name, args)
	if err != nil {
		return "", err
	}
	if res.IsError {
		// Tool-level failure: surface the text as an error so the executor wraps
		// it as a tool_result with IsError=true (the s03 non-crashing discipline).
		return "", fmt.Errorf("%s", res.Text())
	}
	return res.Text(), nil
}

// DiscoverTools is the end-to-end "connect a server's tools into the agent"
// helper, modeled on cline's connectToServer → fetchToolsList sequence: run the
// handshake, list the tools, and wrap each as a registry-ready Tool. Merging the
// returned slice into a Registry is what extends the agent at runtime.
func DiscoverTools(c *McpClient) ([]Tool, error) {
	if _, err := c.Initialize(); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	schemas, err := c.ListTools()
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	tools := make([]Tool, 0, len(schemas))
	for _, s := range schemas {
		tools = append(tools, &mcpTool{client: c, schema: s})
	}
	return tools, nil
}
