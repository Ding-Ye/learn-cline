# s02 · Streaming Message Parser / 流式消息解析器

> Parse tool calls out of the model's TEXT stream, before the message is finished.
> 在消息还没收完时，就从模型的**文本流**里解析出工具调用。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s02`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 2 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

s01 used the Anthropic **native** `tool_use` block: the API handed us tool calls
as structured JSON. But cline also supports models that *don't* do that — they
write tool calls as XML-ish text in the middle of normal prose:

s01 用的是 Anthropic **原生** `tool_use` 块：API 已经把工具调用拆成结构化 JSON
交给我们了。可 cline 还要支持那些**不**这么做的模型——它们把工具调用当成 XML
文本，混在普通输出里：

```
I'll create the file now.
<write_to_file>
<path>hello.txt</path>
<content>hi</content>
</write_to_file>
```

That text **arrives over a stream**, a few tokens at a time. cline wants to show
the prose live and detect the tool call the instant its tags are complete — so it
needs an **incremental** parser: `feed(chunk)` accumulates, `parse()` re-derives
the block list, and the trailing block is marked **partial** when the stream cut
mid-tag. That is why cline can *stream and act* before the full message lands.
This chapter ports `parseAssistantMessageV2`.

这段文本是**流式到达**的，一次几个 token。cline 想边收边显示文字，并在工具标签
一闭合就立刻识别出调用——所以它需要一个**增量**解析器：`feed(chunk)` 累积，
`parse()` 重新推导出块列表，流在标签中间断掉时把末尾块标成 **partial**。这正是
cline 能在整条消息到齐之前就**边流边动**的原因。本章移植 `parseAssistantMessageV2`。

```
chunk ─▶ feed() ─▶ [accumulated buffer] ─▶ parse() ─▶ []AssistantBlock
                                                        ├─ text
                                                        └─ tool_use{name, params, partial}
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | canonical types: `StreamEvent`, `AssistantBlock` (parser output), plus the s01 wire-shape `Message`/`ContentBlock` for continuity |
| `parser.go` | `StreamingParser` — `feed(chunk)` accumulates; `parse()` runs the index-driven state machine → `[]AssistantBlock`; recognized-tool / param tables; partial-tag + nested-`</content>` handling |
| `main.go` | demo: feeds a sample streamed assistant turn in chunks and prints the parsed blocks (`-chunk N`, `-steps`) |
| `parser_test.go` | 8 tests, fully offline (no LLM, deterministic) |

---

## Try it / 动手试一试

Everything is offline and deterministic — there is no LLM in this chapter.
全程离线、确定性输出——本章没有任何 LLM 调用。

```bash
cd agents/s02-streaming-message-parser

# Feed the sample in 7-byte chunks (splits tags across boundaries on purpose)
go run .

# Watch the block list grow after each chunk
go run . -steps

# Feed it all at once — final parse is identical to the chunked run
go run . -chunk 0

# Run the 8 tests / see each case
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt).
预期输出形态见该文件——本章可逐字复现。

---

## Deliberately omitted / 故意省略

cline's parser also emits `reasoning` blocks, tracks a `call_id` per tool, and
carries a Gemini `signature`; those are continuity details, not the mechanism.
The native-tool-call path (where the provider, not this parser, splits out tool
calls) is surfaced in s05. Turning a parsed `tool_use` into an executed result is
s03; gating it behind human approval is s04.

cline 的解析器还会产出 `reasoning` 块、给每个工具追踪 `call_id`、携带 Gemini 的
`signature`——这些是连续性细节，不是机制本身。原生工具调用那条路（由供应商而非
本解析器拆出工具调用）在 s05 呈现。把解析出的 `tool_use` 执行成结果是 s03，给它
加上人类审批门控是 s04。

See `docs/en/s02-streaming-message-parser.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s02-streaming-message-parser.md`。
