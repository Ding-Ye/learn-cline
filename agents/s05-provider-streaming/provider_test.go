package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// drain replays raw SSE text through a decoder and collects every chunk. We use
// a plain strings.Reader as the "fake reader" so no network is involved.
func drain(decode func(context.Context, *strings.Reader, chan<- StreamChunk), raw string) []StreamChunk {
	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		decode(context.Background(), strings.NewReader(raw), out)
	}()
	var got []StreamChunk
	for c := range out {
		got = append(got, c)
	}
	return got
}

func drainAnthropic(raw string) []StreamChunk {
	return drain(func(ctx context.Context, r *strings.Reader, out chan<- StreamChunk) {
		decodeAnthropicSSE(ctx, r, out)
	}, raw)
}

func drainOpenAI(raw string) []StreamChunk {
	return drain(func(ctx context.Context, r *strings.Reader, out chan<- StreamChunk) {
		decodeOpenAISSE(ctx, r, out)
	}, raw)
}

const anthropicFixture = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":11,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"read_file"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8}}

event: message_stop
data: {"type":"message_stop"}
`

// 1. The Anthropic SSE decoder turns message_start / content_block_delta /
//    message_stop into ordered, normalized chunks ending in a single done.
func TestAnthropicSSEDecodeOrder(t *testing.T) {
	got := drainAnthropic(anthropicFixture)

	var types []string
	for _, c := range got {
		types = append(types, c.Type)
	}
	want := []string{
		ChunkUsage,         // message_start (input tokens)
		ChunkText,          // "Hello"
		ChunkText,          // ", world"
		ChunkToolUseStart,  // read_file opens
		ChunkToolUseDelta,  // {"path":
		ChunkToolUseDelta,  // "a.go"}
		ChunkUsage,         // message_delta (output tokens)
		ChunkDone,          // message_stop
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("chunk order\n got: %v\nwant: %v", types, want)
	}
	if got[len(got)-1].Type != ChunkDone {
		t.Fatalf("stream must end with done, got %q", got[len(got)-1].Type)
	}
}

// 2. A tool_use surfaces a start chunk (id+name) plus delta chunks whose
//    fragments concatenate into the full input JSON.
func TestAnthropicToolUseAssembly(t *testing.T) {
	got := drainAnthropic(anthropicFixture)

	var start *StreamChunk
	var args strings.Builder
	for i := range got {
		switch got[i].Type {
		case ChunkToolUseStart:
			c := got[i]
			start = &c
		case ChunkToolUseDelta:
			args.WriteString(got[i].InputJSON)
		}
	}
	if start == nil {
		t.Fatal("no tool_use_start chunk")
	}
	if start.ToolCallID != "toolu_9" || start.ToolName != "read_file" {
		t.Fatalf("tool start = %+v, want id=toolu_9 name=read_file", start)
	}
	if got := args.String(); got != `{"path":"a.go"}` {
		t.Fatalf("assembled input = %q, want %q", got, `{"path":"a.go"}`)
	}
}

// 3. assembleResponse folds streamed text deltas into a single text block.
func TestTextAssembledFromChunks(t *testing.T) {
	out := make(chan StreamChunk, 4)
	out <- StreamChunk{Type: ChunkText, Text: "foo "}
	out <- StreamChunk{Type: ChunkText, Text: "bar "}
	out <- StreamChunk{Type: ChunkText, Text: "baz"}
	out <- StreamChunk{Type: ChunkDone}
	close(out)

	resp, err := assembleResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" {
		t.Fatalf("want one text block, got %+v", resp.Content)
	}
	if resp.Content[0].Text != "foo bar baz" {
		t.Fatalf("assembled text = %q, want %q", resp.Content[0].Text, "foo bar baz")
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", resp.StopReason)
	}
}

// 3b. End-to-end: the full Anthropic fixture assembles into a text block + a
//     tool_use block with parsed input, and stop_reason flips to tool_use.
func TestAnthropicAssembledResponse(t *testing.T) {
	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		for _, c := range drainAnthropic(anthropicFixture) {
			out <- c
		}
	}()
	resp, err := assembleResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("want text + tool_use, got %d blocks: %+v", len(resp.Content), resp.Content)
	}
	if resp.Content[0].Type != "text" || resp.Content[0].Text != "Hello, world" {
		t.Fatalf("text block = %+v", resp.Content[0])
	}
	tu := resp.Content[1]
	if tu.Type != "tool_use" || tu.Name != "read_file" || tu.ID != "toolu_9" {
		t.Fatalf("tool_use block = %+v", tu)
	}
	if tu.Input["path"] != "a.go" {
		t.Fatalf("tool input path = %v, want a.go", tu.Input["path"])
	}
}

// 4. Usage is captured: input tokens from message_start, output tokens from
//    message_delta.
func TestUsageCaptured(t *testing.T) {
	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		for _, c := range drainAnthropic(anthropicFixture) {
			out <- c
		}
	}()
	resp, err := assembleResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want 11", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 8 {
		t.Fatalf("output tokens = %d, want 8", resp.Usage.OutputTokens)
	}
}

// 5. The OpenAI-compatible delta protocol decodes: id+name arrive on the first
//    tool-call frame, argument fragments accumulate across later frames keyed
//    by stream index, and [DONE] terminates.
func TestOpenAISSEDecode(t *testing.T) {
	const fixture = `data: {"choices":[{"delta":{"content":"Hi "}}]}

data: {"choices":[{"delta":{"content":"there"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_7","function":{"name":"list_files","arguments":"{\"dir\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\".\"}"}}]}}]}

data: {"usage":{"prompt_tokens":5,"completion_tokens":9}}

data: [DONE]
`
	got := drainOpenAI(fixture)
	resp, err := assembleResponse(channelOf(got))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("want text + tool_use, got %+v", resp.Content)
	}
	if resp.Content[0].Text != "Hi there" {
		t.Fatalf("text = %q, want %q", resp.Content[0].Text, "Hi there")
	}
	tu := resp.Content[1]
	if tu.Type != "tool_use" || tu.ID != "call_7" || tu.Name != "list_files" {
		t.Fatalf("tool_use = %+v", tu)
	}
	if tu.Input["dir"] != "." {
		t.Fatalf("tool input dir = %v, want .", tu.Input["dir"])
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v, want in=5 out=9", resp.Usage)
	}
	if got[len(got)-1].Type != ChunkDone {
		t.Fatalf("stream must end with done, got %q", got[len(got)-1].Type)
	}
}

// 6. The factory returns the right concrete provider by name and errors on an
//    unknown one (no silent fallback to Anthropic).
func TestFactorySelectsProvider(t *testing.T) {
	a, err := buildHandler("anthropic", ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(*AnthropicProvider); !ok {
		t.Fatalf("anthropic -> %T, want *AnthropicProvider", a)
	}

	for _, name := range []string{"openai", "deepseek", "moonshot", "qwen", "groq", "openrouter", "local"} {
		p, err := buildHandler(name, ProviderConfig{APIKey: "k"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := p.(*OpenAIProvider); !ok {
			t.Fatalf("%s -> %T, want *OpenAIProvider", name, p)
		}
	}

	if _, err := buildHandler("bananas", ProviderConfig{APIKey: "k"}); err == nil {
		t.Fatal("unknown provider must error, not silently fall back")
	}
}

// 7. A non-2xx status errors at stream-open time, before any channel is handed
//    back — the caller shouldn't have to drain to learn the request failed.
func TestErrorOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	a := NewAnthropicProvider("nope", "claude-x")
	a.baseURL = srv.URL

	ch, err := a.CreateMessageStream(context.Background(), CreateMessageRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected an error on 401, got nil")
	}
	if ch != nil {
		t.Fatal("no channel should be returned on error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q should mention status 401", err)
	}
}

// 7b. The happy path over a fake HTTP server: a streaming 200 yields chunks.
func TestStreamOverFakeServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicFixture))
	}))
	defer srv.Close()

	a := NewAnthropicProvider("k", "claude-x")
	a.baseURL = srv.URL

	ch, err := a.CreateMessageStream(context.Background(), CreateMessageRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := assembleResponse(ch)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(resp.Content) != 2 {
		t.Fatalf("unexpected assembled response: %+v", resp)
	}
}

func channelOf(chunks []StreamChunk) <-chan StreamChunk {
	out := make(chan StreamChunk, len(chunks))
	for _, c := range chunks {
		out <- c
	}
	close(out)
	return out
}
