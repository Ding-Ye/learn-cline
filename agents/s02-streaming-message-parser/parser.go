package main

import "strings"

// ---------------------------------------------------------------------------
// StreamingParser — an incremental port of cline's parseAssistantMessageV2
// (apps/vscode/src/core/assistant-message/parse-assistant-message.ts L28-240).
//
// THE PROBLEM IT SOLVES
// ---------------------
// Generic models (anything without native tool calling) ask to run a tool by
// writing XML-ish text in the middle of their normal prose:
//
//	I'll create the file now.
//	<write_to_file>
//	<path>hello.txt</path>
//	<content>hi</content>
//	</write_to_file>
//
// That text ARRIVES OVER A STREAM, a few tokens at a time. cline wants to react
// before the message is finished — show the prose live, and the instant a tool's
// tags are complete, hand the tool call off for approval. So the parser must:
//
//   1. consume chunks and remember everything seen so far (feed),
//   2. re-derive the full block list from the accumulated buffer (parse),
//   3. mark the trailing block "partial" when the stream cut mid-tag — a partial
//      <write_to_file> is what lets the UI say "about to write a file…".
//
// WHY RE-PARSE THE WHOLE BUFFER EACH TIME (instead of resuming mid-state)?
// Upstream parseAssistantMessageV2 is a PURE function of the accumulated string:
// cline calls it on the growing buffer after every delta. Re-deriving from the
// whole buffer is what makes partial-tag handling correct across ANY chunk
// boundary — a tag split as "<write_" + "to_file>" is invisible to the parser,
// because it never sees the split, only the concatenation. This is the single
// most important design choice in the chapter, and we copy it exactly.
// ---------------------------------------------------------------------------

// recognizedTools is the set of tool names whose opening tag (<name>) starts a
// tool_use block. Upstream derives this from getToolUseNames() (the
// ClineDefaultTool enum). We hard-code the handful this chapter exercises; an
// unknown tag like <thinking> is therefore NOT a tool and stays text.
var recognizedTools = []string{
	"write_to_file",
	"replace_in_file",
	"read_file",
	"execute_command",
	"list_files",
	"search_files",
	"ask_followup_question",
	"attempt_completion",
	"use_mcp_tool",
}

// toolParamNames is the set of <param> tags recognized INSIDE a tool block.
// Upstream lists ~50 (assistant-message/index.ts toolParamNames); these are the
// ones the recognized tools above actually use. A tag inside a tool that is not
// in this set is treated as literal content, not a parameter boundary.
var toolParamNames = []string{
	"path", "content", "diff", "command", "requires_approval",
	"regex", "file_pattern", "recursive", "question", "options",
	"result", "server_name", "tool_name", "arguments",
}

// StreamingParser accumulates streamed text and parses it into AssistantBlocks.
// It is the s02 incarnation of the catalog's AssistantMessageParser interface
// (Append/Blocks/Reset); we name the methods feed/parse to match the chapter's
// "feed(chunk) … parse()" framing while keeping the same shape.
type StreamingParser struct {
	buf strings.Builder // everything fed so far (the accumulated assistant message)
}

// NewStreamingParser returns an empty parser ready to be fed.
func NewStreamingParser() *StreamingParser { return &StreamingParser{} }

// feed appends one streamed chunk to the buffer. It does NOT parse — parsing is
// deferred to parse() so a tag split across feed() calls is never observed in
// its split form. (Catalog name: Append.)
func (p *StreamingParser) feed(chunk string) { p.buf.WriteString(chunk) }

// Reset clears the buffer so the parser can be reused for a new message.
func (p *StreamingParser) Reset() { p.buf.Reset() }

// Buffer exposes the accumulated text (handy for demos/tests).
func (p *StreamingParser) Buffer() string { return p.buf.String() }

// parse derives the current block list from the whole accumulated buffer. The
// trailing block is flagged Partial when the buffer ends mid-block (the stream
// hasn't delivered the closing tag yet). Calling parse() repeatedly as more is
// fed yields progressively more complete blocks, and the FINAL parse() (after
// the stream's last chunk) equals parsing the whole string at once. (Catalog
// name: Blocks.)
//
// This is a direct port of parseAssistantMessageV2's index-driven scan. We walk
// the buffer one rune-as-byte index at a time and, at each position, ask: does
// the substring ENDING here match a known opening/closing tag? State is held in
// start-indices (not an accumulator string), and we slice out content only when
// a block completes. The three states are: in-text, in-tool, in-param.
func (p *StreamingParser) parse() []AssistantBlock {
	msg := p.buf.String()
	var blocks []AssistantBlock

	textStart := 0  // index where the current text run began
	var inText bool // are we accumulating a text block?

	var tool *AssistantBlock // the open tool_use, or nil
	toolContentStart := 0    // index right after the tool's opening tag

	var paramName string // the open <param> name, or "" if none
	paramValueStart := 0 // index right after the param's opening tag

	n := len(msg)
	for i := 0; i < n; i++ {
		// --- State: inside a tool PARAMETER ---
		if tool != nil && paramName != "" {
			closeTag := "</" + paramName + ">"
			if endsWith(msg, i, closeTag) {
				// Param closed: slice value between the open and close tags.
				value := strings.TrimSpace(msg[paramValueStart : i-len(closeTag)+1])
				tool.Params[paramName] = value
				paramName = "" // back to tool-content state
				// fallthrough: position i may also close the tool / start a param
			} else {
				continue // still inside the param value
			}
		}

		// --- State: inside a TOOL but not a specific param ---
		if tool != nil && paramName == "" {
			// Starting a new <param>?
			started := false
			for _, name := range toolParamNames {
				openTag := "<" + name + ">"
				if endsWith(msg, i, openTag) {
					paramName = name
					paramValueStart = i + 1 // value begins after the tag
					started = true
					break
				}
			}
			if started {
				continue
			}

			// Closing this tool?
			toolClose := "</" + tool.ToolName + ">"
			if endsWith(msg, i, toolClose) {
				// Special case: write_to_file's <content> may itself contain text
				// that LOOKS like a closing tag (it's a file body!). If the param
				// scan above missed it, recover the content using lastIndexOf of
				// </content> so nested tag-like text survives. Upstream does the
				// same for write_to_file (and new_rule).
				inner := msg[toolContentStart : i-len(toolClose)+1]
				if tool.ToolName == "write_to_file" && strings.Contains(inner, "<content>") {
					cs := strings.Index(inner, "<content>")
					ce := strings.LastIndex(inner, "</content>")
					if cs != -1 && ce != -1 && ce > cs {
						tool.Params["content"] = strings.TrimSpace(inner[cs+len("<content>") : ce])
					}
				}
				tool.Partial = false // closing tag seen → complete
				blocks = append(blocks, *tool)
				tool = nil
				textStart = i + 1 // any text resumes after this tag
				continue
			}
			// Otherwise we're still inside the tool body; keep scanning.
			continue
		}

		// --- State: TEXT / looking for a tool to start ---
		started := false
		for _, name := range recognizedTools {
			openTag := "<" + name + ">"
			if endsWith(msg, i, openTag) {
				// Close any text run that was building, trimming off the tag we
				// just matched (it belongs to the tool, not the text).
				if inText {
					content := strings.TrimSpace(msg[textStart : i-len(openTag)+1])
					if content != "" {
						blocks = append(blocks, AssistantBlock{Type: "text", Text: content, Partial: false})
					}
					inText = false
				}
				// Open the tool. Assume partial until its closing tag is found.
				tool = &AssistantBlock{
					Type:     "tool_use",
					ToolName: name,
					Params:   map[string]string{},
					Partial:  true,
				}
				toolContentStart = i + 1
				started = true
				break
			}
		}
		if started {
			continue
		}
		if !inText {
			textStart = i // a fresh text run begins here
			inText = true
		}
		// else: keep accumulating text; its content is sliced on close.
	}

	// --- Finalization: the buffer ended; flush whatever block is open ---

	// An open param inside an open tool: take the rest of the buffer as its value.
	if tool != nil && paramName != "" {
		tool.Params[paramName] = strings.TrimSpace(msg[paramValueStart:])
	}
	// An open tool stays partial (its closing tag never arrived).
	if tool != nil {
		blocks = append(blocks, *tool)
	} else if inText {
		// Trailing text; it is partial because the stream ended here.
		content := strings.TrimSpace(msg[textStart:])
		if content != "" {
			blocks = append(blocks, AssistantBlock{Type: "text", Text: content, Partial: true})
		}
	}

	return blocks
}

// endsWith reports whether `tag` is the substring of `s` that ENDS at index `i`
// (inclusive). This is the Go equivalent of upstream's
// `s.startsWith(tag, i - tag.length + 1)` guarded by a length check: it lets the
// scan detect a complete tag the moment its last character is read, which is how
// a single forward pass recognizes both opening and closing tags.
func endsWith(s string, i int, tag string) bool {
	start := i - len(tag) + 1
	if start < 0 {
		return false
	}
	return s[start:i+1] == tag
}
