package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Wire shape (Anthropic Messages API).
//
// cline talks to ~40 providers, but every one is normalized to ONE internal
// vocabulary (apps/vscode/src/core/api). We do the same: these types ARE our
// internal block model, and each provider translates at the boundary. Keeping
// one model means the agent loop never has to know which provider answered.
//
// s01-s04 used a BUFFERED provider (one request, one whole response). s05 adds
// STREAMING: the model emits a sequence of small chunks over Server-Sent
// Events, and we normalize every vendor's chunks into one StreamChunk type.
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
	StopReason string         `json:"stop_reason"` // "end_turn" | "tool_use" | ...
	Usage      Usage          `json:"usage"`
}

// ---------------------------------------------------------------------------
// Streaming events.
//
// StreamChunk is the Go equivalent of cline's ApiStreamChunk union
// (apps/vscode/src/core/api/transform/stream.ts). cline yields these from an
// async generator; Go's idiom is a receive-only channel. Every provider, no
// matter the on-wire format, emits this SAME chunk type — that is the whole
// point of the abstraction.
// ---------------------------------------------------------------------------

// Chunk type tags. The agent loop switches on these, never on a vendor format.
const (
	ChunkText         = "text"           // a text delta
	ChunkToolUseStart = "tool_use_start" // a tool_use block opened (id + name)
	ChunkToolUseDelta = "tool_use_delta" // a partial JSON fragment of tool input
	ChunkUsage        = "usage"          // token accounting
	ChunkDone         = "done"           // the message is complete
)

// StreamChunk is one normalized event off the wire.
type StreamChunk struct {
	Type string `json:"type"`

	// Type == ChunkText
	Text string `json:"text,omitempty"`

	// Type == ChunkToolUseStart / ChunkToolUseDelta
	ToolCallID string `json:"tool_call_id,omitempty"` // start + delta
	ToolName   string `json:"tool_name,omitempty"`    // start
	InputJSON  string `json:"input_json,omitempty"`   // delta (partial JSON)

	// Type == ChunkUsage
	Usage *Usage `json:"usage,omitempty"`
}

// Provider abstracts the LLM call so the loop, tests, and other providers can
// be swapped without touching the loop.
//
// CreateMessageStream is the primary, streaming path (cline's createMessage
// IS a generator). CreateMessage is a buffered convenience used by s01-style
// loops — its default implementation here just drains the stream and folds the
// chunks back into one response, so a provider only has to implement streaming
// once.
type Provider interface {
	CreateMessageStream(ctx context.Context, req CreateMessageRequest) (<-chan StreamChunk, error)
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// assembleResponse folds a stream of chunks into one buffered response. This is
// the non-stream fallback every provider shares: stream once, collect text and
// tool_use blocks (assembling each tool's partial-JSON input as it arrives),
// and capture usage. Mirrors what cline's loop does as it consumes ApiStream.
func assembleResponse(ch <-chan StreamChunk) (*CreateMessageResponse, error) {
	out := &CreateMessageResponse{Role: "assistant", StopReason: "end_turn"}
	var text strings.Builder

	// Preserve tool-call order while accumulating each one's partial input JSON.
	order := []string{}
	args := map[string]*strings.Builder{}
	names := map[string]string{}
	// active is the id of the tool call currently streaming. Anthropic deltas
	// carry only the JSON fragment (no id), so we attribute them to whichever
	// tool_use_start opened most recently; OpenAI deltas DO carry an id, which
	// takes precedence when present.
	active := ""

	for c := range ch {
		switch c.Type {
		case ChunkText:
			text.WriteString(c.Text)
		case ChunkToolUseStart:
			if _, seen := args[c.ToolCallID]; !seen {
				order = append(order, c.ToolCallID)
				args[c.ToolCallID] = &strings.Builder{}
			}
			names[c.ToolCallID] = c.ToolName
			active = c.ToolCallID
		case ChunkToolUseDelta:
			id := c.ToolCallID
			if id == "" {
				id = active
			}
			if b, ok := args[id]; ok {
				b.WriteString(c.InputJSON)
			}
		case ChunkUsage:
			if c.Usage != nil {
				if c.Usage.InputTokens > 0 {
					out.Usage.InputTokens = c.Usage.InputTokens
				}
				if c.Usage.OutputTokens > 0 {
					out.Usage.OutputTokens = c.Usage.OutputTokens
				}
			}
		}
	}

	if text.Len() > 0 {
		out.Content = append(out.Content, ContentBlock{Type: "text", Text: text.String()})
	}
	for _, id := range order {
		input := map[string]interface{}{}
		raw := args[id].String()
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				input = map[string]interface{}{"_raw_arguments": raw}
			}
		}
		out.Content = append(out.Content, ContentBlock{
			Type: "tool_use", ID: id, Name: names[id], Input: input,
		})
	}
	if len(order) > 0 {
		out.StopReason = "tool_use"
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// AnthropicProvider — native Messages API streaming.
// Mirrors apps/vscode/src/core/api/providers/anthropic.ts createMessage.
// ---------------------------------------------------------------------------

type AnthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.anthropic.com",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	ch, err := a.CreateMessageStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return assembleResponse(ch)
}

// CreateMessageStream POSTs with stream:true and decodes the Anthropic SSE
// event protocol into normalized StreamChunks.
func (a *AnthropicProvider) CreateMessageStream(ctx context.Context, req CreateMessageRequest) (<-chan StreamChunk, error) {
	if req.Model == "" {
		req.Model = a.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	// stream:true is what flips the API from buffered to SSE.
	wire := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   req.Messages,
		"stream":     true,
	}
	if req.System != "" {
		wire["system"] = req.System
	}
	if len(req.Tools) > 0 {
		wire["tools"] = req.Tools
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	// A non-2xx must error BEFORE we hand back a channel — the caller should
	// not have to drain a stream to discover the request was rejected.
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic API %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	out := make(chan StreamChunk)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		decodeAnthropicSSE(ctx, resp.Body, out)
	}()
	return out, nil
}

// decodeAnthropicSSE reads the SSE body line-by-line and translates Anthropic's
// event types into normalized chunks. The Anthropic stream looks like:
//
//	event: message_start
//	data: {"type":"message_start","message":{"usage":{...}}}
//
//	event: content_block_start
//	data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}
//	...
//	event: message_stop
//	data: {"type":"message_stop"}
//
// We only read the `data:` JSON; the `event:` line is redundant with the
// payload's own "type" field, exactly as cline's switch keys off chunk.type.
func decodeAnthropicSSE(ctx context.Context, r io.Reader, out chan<- StreamChunk) {
	// Anthropic's input_json_delta frames don't repeat the tool id; we remember
	// the id from the matching content_block_start and stamp it on each delta,
	// mirroring cline's lastStartedToolCall (anthropic.ts L181/L277).
	activeToolID := ""
	for ev := range sseEvents(r) {
		if ev.data == "" {
			continue
		}
		var p anthropicEvent
		if err := json.Unmarshal([]byte(ev.data), &p); err != nil {
			continue // skip malformed lines; a real client would surface this
		}
		switch p.Type {
		case "message_start":
			// Carries the INPUT token count up front (cache reads/writes/input).
			if p.Message != nil {
				u := p.Message.Usage
				if !send(ctx, out, StreamChunk{
					Type:  ChunkUsage,
					Usage: &Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens},
				}) {
					return
				}
			}
		case "content_block_start":
			// A tool_use block opens with its id + name; text blocks carry no
			// useful payload here (text arrives via deltas).
			if p.ContentBlock != nil && p.ContentBlock.Type == "tool_use" {
				activeToolID = p.ContentBlock.ID
				if !send(ctx, out, StreamChunk{
					Type:       ChunkToolUseStart,
					ToolCallID: p.ContentBlock.ID,
					ToolName:   p.ContentBlock.Name,
				}) {
					return
				}
			}
		case "content_block_delta":
			if p.Delta == nil {
				break
			}
			switch p.Delta.Type {
			case "text_delta":
				if !send(ctx, out, StreamChunk{Type: ChunkText, Text: p.Delta.Text}) {
					return
				}
			case "input_json_delta":
				// Tool input streams as PARTIAL JSON fragments; the consumer
				// concatenates them and parses once the block stops.
				if !send(ctx, out, StreamChunk{
					Type:       ChunkToolUseDelta,
					ToolCallID: activeToolID,
					InputJSON:  p.Delta.PartialJSON,
				}) {
					return
				}
			}
		case "message_delta":
			// Running output-token count; stop_reason also lives here.
			if !send(ctx, out, StreamChunk{
				Type:  ChunkUsage,
				Usage: &Usage{OutputTokens: p.Usage.OutputTokens},
			}) {
				return
			}
		case "message_stop":
			send(ctx, out, StreamChunk{Type: ChunkDone})
			return
		}
	}
	// Stream ended without an explicit message_stop (e.g. a truncated fixture):
	// still emit a terminal done so consumers can rely on it.
	send(ctx, out, StreamChunk{Type: ChunkDone})
}

// anthropicEvent is the subset of the SSE payload we care about.
type anthropicEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Usage Usage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	Usage Usage `json:"usage"`
}

// ---------------------------------------------------------------------------
// SSE line reader (shared by both providers).
// ---------------------------------------------------------------------------

// sseEvent is one Server-Sent Event: the accumulated `data:` payload (multiple
// data lines are joined with newlines, per the SSE spec) and its `event:` name.
type sseEvent struct {
	event string
	data  string
}

// sseEvents turns a raw SSE byte stream into a channel of events. An event ends
// at a blank line; `data:` lines accumulate, `event:` names the event, and
// comment lines (starting with ':') are ignored.
func sseEvents(r io.Reader) <-chan sseEvent {
	ch := make(chan sseEvent)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		// Tool-input JSON can be large; raise the line cap well above the 64KB default.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var cur sseEvent
		flush := func() {
			if cur.data != "" || cur.event != "" {
				ch <- cur
			}
			cur = sseEvent{}
		}
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				flush() // blank line dispatches the current event
			case strings.HasPrefix(line, ":"):
				// comment / heartbeat — ignore
			case strings.HasPrefix(line, "event:"):
				cur.event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				d := strings.TrimSpace(line[len("data:"):])
				if cur.data == "" {
					cur.data = d
				} else {
					cur.data += "\n" + d
				}
			}
		}
		flush() // dispatch a trailing event with no final blank line
	}()
	return ch
}

// send writes to out unless ctx is cancelled; returns false if it should stop.
func send(ctx context.Context, out chan<- StreamChunk, c StreamChunk) bool {
	select {
	case out <- c:
		return true
	case <-ctx.Done():
		return false
	}
}
