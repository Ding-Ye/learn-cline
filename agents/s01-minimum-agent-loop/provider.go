package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Wire shape (Anthropic Messages API).
//
// cline talks to ~40 providers, but every one is normalized to the Anthropic
// block model internally (apps/vscode/src/core/api). We do the same: these
// types ARE our internal vocabulary, and the OpenAI provider translates at the
// boundary (see provider_openai.go). Keeping one block model means the loop in
// loop.go never has to know which provider answered.
// ---------------------------------------------------------------------------

// Message is one turn in the conversation. The API takes an alternating
// user / assistant list plus an optional system prompt.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type; the JSON encoder relies on `omitempty` to suppress fields that
// don't apply to a given block type.
//
// s01 uses the THREE block types the loop needs:
//
//	"text"        — plain assistant prose (and the initial user prompt)
//	"tool_use"    — the model asks to run a tool (id + name + input)
//	"tool_result" — we feed a tool's output back (tool_use_id + content)
type ContentBlock struct {
	Type string `json:"type"`

	// type == "text"
	Text string `json:"text,omitempty"`

	// type == "tool_use"
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	// type == "tool_result"
	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
	IsError     bool        `json:"is_error,omitempty"`
}

// ToolSchema is a tool advertised to the model in the request.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type CreateMessageRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
}

type CreateMessageResponse struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"` // "end_turn" | "tool_use" | "max_tokens" | ...
	Usage      Usage          `json:"usage"`
}

// Provider abstracts the LLM call so the loop, tests, and later sessions can
// swap Anthropic for OpenAI / a fake without touching loop.go.
//
// s01 uses the BUFFERED CreateMessage (one request, one whole response). cline
// itself streams (its ApiStream is an async generator); s05 teaches that. The
// loop shape is identical either way — buffering just lets us focus on the loop.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// ---------------------------------------------------------------------------
// AnthropicProvider — the native API. Mirrors
// apps/vscode/src/core/api/providers/anthropic.ts, minus streaming.
// ---------------------------------------------------------------------------

type AnthropicProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	if req.Model == "" {
		req.Model = a.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody))
	}

	var out CreateMessageResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%s)", err, string(respBody))
	}
	return &out, nil
}
