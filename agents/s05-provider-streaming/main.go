// Command s05 demonstrates cline's provider-streaming abstraction: every LLM
// vendor is normalized to one async stream of StreamChunks (text deltas,
// tool_use start/delta, usage, done), behind a buildHandler factory.
//
// By default it replays a RECORDED Anthropic SSE fixture through the real
// decoder — no network, no API key — so you can watch the chunk pipeline.
// With -live it streams from a real provider you select by profile.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

// recordedAnthropicSSE is a real-shaped Anthropic Messages SSE transcript: a
// short text answer, then a tool_use whose JSON input streams in two
// fragments, then usage + stop. The offline demo feeds this through the SAME
// decodeAnthropicSSE the live provider uses, so the pipeline is identical.
const recordedAnthropicSSE = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":42,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"read that file."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"read_file"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":17}}

event: message_stop
data: {"type":"message_stop"}
`

func main() {
	live := flag.Bool("live", false, "stream from a real provider (needs an API key)")
	provider := flag.String("provider", "anthropic", "provider profile: anthropic|openai|deepseek|moonshot|qwen|groq|openrouter|local")
	model := flag.String("model", "", "model id (defaults per provider)")
	baseURL := flag.String("base-url", "", "override base URL (OpenAI-compatible providers)")
	prompt := flag.String("prompt", "Read main.go and summarize it.", "user prompt (live mode)")
	flag.Parse()

	if !*live {
		runOffline()
		return
	}

	if err := runLive(*provider, *model, *baseURL, *prompt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runOffline replays the recorded fixture through the decoder and narrates each
// normalized chunk as it arrives, then folds them into one buffered response.
func runOffline() {
	fmt.Println("== offline demo: replaying a recorded Anthropic SSE stream ==")
	ctx := context.Background()

	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		decodeAnthropicSSE(ctx, strings.NewReader(recordedAnthropicSSE), out)
	}()

	// Tee: print the live chunk narration AND collect for the final response.
	collected := make(chan StreamChunk)
	go func() {
		defer close(collected)
		for c := range out {
			switch c.Type {
			case ChunkText:
				fmt.Printf("  [text]  %q\n", c.Text)
			case ChunkToolUseStart:
				fmt.Printf("  [tool]  start id=%s name=%s\n", c.ToolCallID, c.ToolName)
			case ChunkToolUseDelta:
				fmt.Printf("  [tool]  input += %q\n", c.InputJSON)
			case ChunkUsage:
				fmt.Printf("  [usage] in=%d out=%d\n", c.Usage.InputTokens, c.Usage.OutputTokens)
			case ChunkDone:
				fmt.Printf("  [done]\n")
			}
			collected <- c
		}
	}()

	resp, _ := assembleResponse(collected)
	fmt.Println("\n== assembled response ==")
	fmt.Printf("stop_reason: %s  usage: in=%d out=%d\n", resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			fmt.Printf("text: %q\n", b.Text)
		case "tool_use":
			fmt.Printf("tool_use: %s%v\n", b.Name, b.Input)
		}
	}
}

// envKey returns the conventional API-key env var for a provider profile.
func envKey(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "moonshot":
		return "MOONSHOT_API_KEY"
	case "qwen":
		return "DASHSCOPE_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "local":
		return "OPENAI_API_KEY"
	default:
		return "API_KEY"
	}
}

func runLive(provider, model, baseURL, prompt string) error {
	key := os.Getenv(envKey(provider))
	if key == "" && provider != "local" {
		return fmt.Errorf("set %s for -provider %s", envKey(provider), provider)
	}

	p, err := buildHandler(provider, ProviderConfig{APIKey: key, Model: model, BaseURL: baseURL})
	if err != nil {
		return err
	}

	req := CreateMessageRequest{
		Model:    model,
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: prompt}}}},
	}

	fmt.Printf("== live stream from %s ==\n", provider)
	ch, err := p.CreateMessageStream(context.Background(), req)
	if err != nil {
		return err
	}
	for c := range ch {
		switch c.Type {
		case ChunkText:
			fmt.Print(c.Text)
		case ChunkToolUseStart:
			fmt.Printf("\n[tool %s]", c.ToolName)
		case ChunkToolUseDelta:
			fmt.Print(c.InputJSON)
		case ChunkUsage:
			// usage chunks are noisy mid-stream; ignore in the live demo
		case ChunkDone:
			fmt.Println("\n[done]")
		}
	}
	return nil
}
