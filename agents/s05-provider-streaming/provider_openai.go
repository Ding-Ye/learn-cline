package main

import (
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
// OpenAIProvider — streaming against ANY OpenAI-compatible Chat Completions
// API (OpenAI, DeepSeek, Moonshot/Kimi, Qwen, Groq, OpenRouter, local
// vLLM/SGLang, ...). Mirrors apps/vscode/src/core/api/providers/openai.ts.
//
// The on-wire format differs from Anthropic's, but the OUTPUT is identical:
// the same StreamChunk type. That is the entire value of the abstraction —
// the loop above never learns which provider answered. The translation lives
// here at the boundary, in both directions:
//
//	CreateMessageRequest (Anthropic block model) → OpenAI chat request
//	OpenAI SSE delta frames               → normalized StreamChunk
// ---------------------------------------------------------------------------

type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *OpenAIProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	ch, err := o.CreateMessageStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return assembleResponse(ch)
}

// CreateMessageStream POSTs to /chat/completions with stream:true and decodes
// the OpenAI SSE delta protocol into normalized StreamChunks.
func (o *OpenAIProvider) CreateMessageStream(ctx context.Context, req CreateMessageRequest) (<-chan StreamChunk, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	wire := translateRequestToOpenAI(req, model, maxTokens)
	wire["stream"] = true
	// include_usage asks the API to append a final chunk carrying token counts
	// (OpenAI omits usage from streamed responses otherwise).
	wire["stream_options"] = map[string]interface{}{"include_usage": true}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai-compat API %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	out := make(chan StreamChunk)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		decodeOpenAISSE(ctx, resp.Body, out)
	}()
	return out, nil
}

// decodeOpenAISSE reads OpenAI's SSE body and translates each delta frame into
// normalized chunks. OpenAI frames look like:
//
//	data: {"choices":[{"delta":{"content":"hi"}}]}
//	data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"p"}}]}}]}
//	data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ath\":\"x\"}"}}]}}]}
//	data: {"usage":{"prompt_tokens":7,"completion_tokens":3}}
//	data: [DONE]
//
// The crucial OpenAI quirk: only the FIRST tool-call frame carries id+name;
// later frames carry only the index and the next argument fragment. We track
// each call by its stream index so deltas land on the right tool_use_start.
func decodeOpenAISSE(ctx context.Context, r io.Reader, out chan<- StreamChunk) {
	// Map a tool call's stream index → the id we announced in its start chunk.
	idByIndex := map[int]string{}

	for ev := range sseEvents(r) {
		data := strings.TrimSpace(ev.data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			send(ctx, out, StreamChunk{Type: ChunkDone})
			return
		}
		var frame openAIStreamFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			continue
		}

		// A trailing usage-only frame (no choices) closes the accounting.
		if frame.Usage != nil {
			if !send(ctx, out, StreamChunk{Type: ChunkUsage, Usage: &Usage{
				InputTokens:  frame.Usage.PromptTokens,
				OutputTokens: frame.Usage.CompletionTokens,
			}}) {
				return
			}
		}
		if len(frame.Choices) == 0 {
			continue
		}
		delta := frame.Choices[0].Delta

		if delta.Content != "" {
			if !send(ctx, out, StreamChunk{Type: ChunkText, Text: delta.Content}) {
				return
			}
		}
		for _, tc := range delta.ToolCalls {
			// First frame for this index: announce a tool_use_start.
			if tc.ID != "" || tc.Function.Name != "" {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("tool_%d", tc.Index)
				}
				idByIndex[tc.Index] = id
				if !send(ctx, out, StreamChunk{
					Type:       ChunkToolUseStart,
					ToolCallID: id,
					ToolName:   tc.Function.Name,
				}) {
					return
				}
			}
			// Every frame (including the first) may carry an argument fragment.
			if tc.Function.Arguments != "" {
				if !send(ctx, out, StreamChunk{
					Type:       ChunkToolUseDelta,
					ToolCallID: idByIndex[tc.Index],
					InputJSON:  tc.Function.Arguments,
				}) {
					return
				}
			}
		}
	}
	send(ctx, out, StreamChunk{Type: ChunkDone})
}

// openAIStreamFrame is the subset of one streamed chat-completion chunk we use.
type openAIStreamFrame struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// translateRequestToOpenAI converts our Anthropic-style request into an OpenAI
// Chat Completions body: the system prompt becomes a {role:"system"} message,
// each Anthropic message expands to 1+ OpenAI messages (assistant tool_use →
// tool_calls; user tool_result → {role:"tool"}), and tools wrap under
// {type:"function", function:{...}}. Returns a map so callers can splice in
// stream/stream_options without a second struct.
func translateRequestToOpenAI(req CreateMessageRequest, model string, maxTokens int) map[string]interface{} {
	msgs := []map[string]interface{}{}
	if req.System != "" {
		msgs = append(msgs, map[string]interface{}{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, anthropicMessageToOpenAI(m)...)
	}

	out := map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   msgs,
	}
	if len(req.Tools) > 0 {
		var tools []map[string]interface{}
		for _, t := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		out["tools"] = tools
	}
	return out
}

// anthropicMessageToOpenAI expands one Anthropic message into 1+ OpenAI ones.
func anthropicMessageToOpenAI(m Message) []map[string]interface{} {
	var out []map[string]interface{}
	switch m.Role {
	case "user":
		var texts []string
		var tools []map[string]interface{}
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_result":
				tools = append(tools, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": b.ToolUseID,
					"content":      stringifyToolResult(b.ToolContent),
				})
			}
		}
		if len(texts) > 0 {
			out = append(out, map[string]interface{}{"role": "user", "content": strings.Join(texts, "\n")})
		}
		out = append(out, tools...)
	case "assistant":
		var texts []string
		var calls []map[string]interface{}
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_use":
				args, _ := json.Marshal(b.Input)
				calls = append(calls, map[string]interface{}{
					"id":       b.ID,
					"type":     "function",
					"function": map[string]interface{}{"name": b.Name, "arguments": string(args)},
				})
			}
		}
		msg := map[string]interface{}{"role": "assistant"}
		if len(texts) > 0 {
			msg["content"] = strings.Join(texts, "\n")
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		out = append(out, msg)
	}
	return out
}

func stringifyToolResult(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	if data, err := json.Marshal(v); err == nil {
		return string(data)
	}
	return fmt.Sprintf("%v", v)
}
