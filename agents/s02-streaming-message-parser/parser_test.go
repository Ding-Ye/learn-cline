package main

import (
	"reflect"
	"testing"
)

// feedAll feeds the whole string in one chunk and returns the final parse.
func feedAll(s string) []AssistantBlock {
	p := NewStreamingParser()
	p.feed(s)
	return p.parse()
}

// Test 1: a message with no tool tags parses to a single text block.
func TestParseTextOnly(t *testing.T) {
	blocks := feedAll("Just some plain prose, no tools here.")
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" {
		t.Fatalf("want text, got %q", blocks[0].Type)
	}
	if blocks[0].Text != "Just some plain prose, no tools here." {
		t.Fatalf("text mismatch: %q", blocks[0].Text)
	}
	// Trailing text is flagged partial: the parser can't know the stream is
	// finished (more prose could still arrive), so it mirrors upstream and marks
	// the last text run partial. Text that ENDS because a tool tag began is the
	// only non-partial text (see TestParseOneToolWithParams blocks[0]).
	if !blocks[0].Partial {
		t.Fatalf("trailing text should be partial (stream may continue)")
	}
}

// Test 2: text followed by one complete tool with two params.
func TestParseOneToolWithParams(t *testing.T) {
	msg := "I'll write it.\n" +
		"<write_to_file>\n<path>a.txt</path>\n<content>hello</content>\n</write_to_file>\nDone."
	blocks := feedAll(msg)
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks (text, tool_use, text), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "I'll write it." {
		t.Fatalf("leading text wrong: %+v", blocks[0])
	}
	tu := blocks[1]
	if tu.Type != "tool_use" || tu.ToolName != "write_to_file" {
		t.Fatalf("tool block wrong: %+v", tu)
	}
	if tu.Partial {
		t.Fatalf("a fully-closed tool must not be partial: %+v", tu)
	}
	if tu.Params["path"] != "a.txt" {
		t.Fatalf("path param wrong: %q", tu.Params["path"])
	}
	if tu.Params["content"] != "hello" {
		t.Fatalf("content param wrong: %q", tu.Params["content"])
	}
	if blocks[2].Type != "text" || blocks[2].Text != "Done." {
		t.Fatalf("trailing text wrong: %+v", blocks[2])
	}
}

// Test 3: two tool calls in one message both parse, in order.
func TestParseMultipleTools(t *testing.T) {
	msg := "<read_file>\n<path>x.go</path>\n</read_file>\n" +
		"now run it\n" +
		"<execute_command>\n<command>go run x.go</command>\n<requires_approval>true</requires_approval>\n</execute_command>"
	blocks := feedAll(msg)
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks (tool, text, tool), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].ToolName != "read_file" || blocks[0].Params["path"] != "x.go" {
		t.Fatalf("first tool wrong: %+v", blocks[0])
	}
	if blocks[1].Type != "text" || blocks[1].Text != "now run it" {
		t.Fatalf("middle text wrong: %+v", blocks[1])
	}
	if blocks[2].ToolName != "execute_command" {
		t.Fatalf("second tool wrong: %+v", blocks[2])
	}
	if blocks[2].Params["command"] != "go run x.go" {
		t.Fatalf("command param wrong: %q", blocks[2].Params["command"])
	}
	if blocks[2].Params["requires_approval"] != "true" {
		t.Fatalf("requires_approval param wrong: %q", blocks[2].Params["requires_approval"])
	}
}

// Test 4: a tool whose tags are split across two feeds still parses correctly.
// The opening tag "<write_to_file>" is deliberately cut mid-tag between chunks;
// because feed() only accumulates and parse() works on the whole buffer, the
// split is invisible to the parser. We also assert the trailing block is partial
// when only the FIRST chunk has been fed.
func TestParseToolSplitAcrossChunks(t *testing.T) {
	full := "<write_to_file>\n<path>split.txt</path>\n<content>data</content>\n</write_to_file>"
	// Cut inside the opening tag: "...<write_" | "to_file>...".
	cut := len("<write_")
	c1, c2 := full[:cut], full[cut:]

	p := NewStreamingParser()
	p.feed(c1)
	mid := p.parse()
	// After only the first partial chunk, nothing is a complete tool yet; the
	// open "<write_" looks like text and must be flagged partial.
	if len(mid) == 1 && mid[0].Type == "tool_use" && !mid[0].Partial {
		t.Fatalf("after first chunk a tool should not yet be complete: %+v", mid)
	}

	p.feed(c2)
	final := p.parse()
	if len(final) != 1 {
		t.Fatalf("want 1 block after both chunks, got %d: %+v", len(final), final)
	}
	tu := final[0]
	if tu.Type != "tool_use" || tu.ToolName != "write_to_file" || tu.Partial {
		t.Fatalf("reassembled tool wrong: %+v", tu)
	}
	if tu.Params["path"] != "split.txt" || tu.Params["content"] != "data" {
		t.Fatalf("params after reassembly wrong: %+v", tu.Params)
	}
}

// Test 5: an unknown / non-tool tag (here <thinking>, and a bang-style </ tag)
// is NOT a recognized tool, so it stays inside a text block verbatim.
func TestUnknownTagTreatedAsText(t *testing.T) {
	msg := "<thinking>let me reason</thinking> then <not_a_tool>x</not_a_tool> done"
	blocks := feedAll(msg)
	if len(blocks) != 1 {
		t.Fatalf("want 1 text block (unknown tags stay text), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" {
		t.Fatalf("want text, got %q", blocks[0].Type)
	}
	if blocks[0].Text != msg {
		t.Fatalf("unknown tags must be preserved verbatim, got %q", blocks[0].Text)
	}
}

// Test 6: write_to_file content containing closing-tag-LOOKING text survives,
// via the lastIndexOf(</content>) recovery path.
func TestWriteToFileContentWithNestedTagText(t *testing.T) {
	body := "line1\n</content> not really the end\nline3"
	msg := "<write_to_file>\n<path>n.txt</path>\n<content>" + body + "</content>\n</write_to_file>"
	blocks := feedAll(msg)
	if len(blocks) != 1 || blocks[0].Type != "tool_use" {
		t.Fatalf("want 1 tool_use, got %+v", blocks)
	}
	got := blocks[0].Params["content"]
	if got != body {
		t.Fatalf("content with nested </content> not preserved:\n got: %q\nwant: %q", got, body)
	}
}

// Test 7: a partial tool (stream cut mid-block) is flagged partial, and any
// param it already received is still captured.
func TestPartialToolFlagged(t *testing.T) {
	// Stream ends after <path> closes but before the tool closes.
	msg := "<write_to_file>\n<path>p.txt</path>\n<content>half"
	blocks := feedAll(msg)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %+v", len(blocks), blocks)
	}
	tu := blocks[0]
	if tu.Type != "tool_use" || !tu.Partial {
		t.Fatalf("an unclosed tool must be partial: %+v", tu)
	}
	if tu.Params["path"] != "p.txt" {
		t.Fatalf("closed param before the cut should still be captured: %+v", tu.Params)
	}
	if tu.Params["content"] != "half" {
		t.Fatalf("open param at the cut should take the rest of the buffer: %q", tu.Params["content"])
	}
}

// Test 8: feeding the same final string one byte at a time yields the SAME final
// blocks as feeding it all at once. This is the core streaming guarantee.
func TestByteByByteEqualsAllAtOnce(t *testing.T) {
	msg := "prologue\n<write_to_file>\n<path>z.txt</path>\n<content>body line\nsecond</content>\n</write_to_file>\nepilogue"

	oneShot := feedAll(msg)

	p := NewStreamingParser()
	for i := 0; i < len(msg); i++ {
		p.feed(msg[i : i+1])
	}
	streamed := p.parse()

	if !reflect.DeepEqual(oneShot, streamed) {
		t.Fatalf("byte-by-byte parse diverged from all-at-once:\n one-shot: %+v\n streamed: %+v", oneShot, streamed)
	}
}
