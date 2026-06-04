package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// sampleResponse is one assistant turn from a generic (non-native-tool-calling)
// model: prose, then an XML tool call cline must parse out of the text stream.
// Note the <content> body deliberately contains a "</content>"-LOOKING string in
// prose terms (a Go comment mentioning tags) to exercise the lastIndexOf path.
const sampleResponse = `I'll create a small greeter for you.
<write_to_file>
<path>greet.go</path>
<content>package main

import "fmt"

// prints a greeting. (mentions of <content> tags inside the body must survive)
func main() { fmt.Println("hi") }
</content>
</write_to_file>
That file is ready.`

func main() {
	// chunk controls how the sample is sliced. The default of 7 bytes splits
	// tags ACROSS boundaries (e.g. "<write_" | "to_file>") on purpose, to show
	// the partial-tag handling. Set -chunk 0 to feed the whole thing at once.
	chunk := flag.Int("chunk", 7, "feed the sample in N-byte chunks (0 = all at once)")
	steps := flag.Bool("steps", false, "print the parse after EACH chunk (watch blocks grow)")
	flag.Parse()

	p := NewStreamingParser()
	chunks := splitChunks(sampleResponse, *chunk)

	fmt.Fprintf(os.Stderr, "[s02] feeding %d chunk(s) of a streamed assistant message\n", len(chunks))
	for idx, c := range chunks {
		p.feed(c)
		if *steps {
			fmt.Fprintf(os.Stderr, "--- after chunk %d (%q) ---\n", idx, c)
			printBlocks(p.parse())
		}
	}

	fmt.Fprintln(os.Stderr, "=== final parse ===")
	printBlocks(p.parse())
}

// splitChunks slices s into size-byte pieces (one piece if size <= 0). Splitting
// by raw bytes is intentional: it can cut a multi-byte tag mid-way, which is
// exactly the chunk-boundary case the parser must tolerate.
func splitChunks(s string, size int) []string {
	if size <= 0 {
		return []string{s}
	}
	var out []string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// printBlocks renders the parser output so you can see text vs tool_use, the
// parsed params, and which trailing block is still partial.
func printBlocks(blocks []AssistantBlock) {
	for i, b := range blocks {
		tag := ""
		if b.Partial {
			tag = " (partial)"
		}
		switch b.Type {
		case "text":
			fmt.Printf("  [%d] text%s: %q\n", i, tag, oneLine(b.Text))
		case "tool_use":
			fmt.Printf("  [%d] tool_use%s: %s\n", i, tag, b.ToolName)
			for _, k := range []string{"path", "content", "diff", "command", "question"} {
				if v, ok := b.Params[k]; ok {
					fmt.Printf("        %s = %q\n", k, oneLine(v))
				}
			}
		}
	}
}

// oneLine collapses newlines so multi-line content prints on a single line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 70 {
		return s[:67] + "..."
	}
	return s
}
