# s07 · File Edit & Diff Application / 文件编辑与差异应用

> Surgical edits via SEARCH/REPLACE blocks, located + spliced + streamed.
> 通过 SEARCH/REPLACE 块做外科手术式编辑：定位 + 拼接 + 流式应用。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s07`, go 1.23, stdlib only).

本章是 **learn-cline** 课程的第 7 章。每一章都是独立的 Go module，只用标准库，
单独可编译、可测试、可运行。

---

## What this teaches / 本章要点

`write_to_file` is blunt — it rewrites a whole file. cline's `replace_in_file`
applies surgical **SEARCH/REPLACE** blocks: the model emits the exact text to
find and the text to put in its place, and the applier locates that text in the
original and splices the replacement over it. Crucially the application is
**stream-aware**, so a partially-arrived diff applies progressively. We port
`constructNewFileContent`: parse the `------- SEARCH` / `=======` / `+++++++ REPLACE`
markers, locate each SEARCH (exact, then line-trimmed), splice the REPLACE, and
support multiple blocks plus an empty SEARCH that creates a new file.

`write_to_file` 很粗暴——整文件重写。cline 的 `replace_in_file` 用外科手术式的
**SEARCH/REPLACE** 块：模型给出要查找的精确文本和要替换成的文本，应用器在原文里
定位再拼接。关键是应用过程是**流式感知**的，半截到达的 diff 也能逐步应用。我们移植
`constructNewFileContent`：解析标记、定位（精确，再退化到逐行去空白）、拼接、支持
多块以及"空 SEARCH = 新建文件"。

```
  diff (lines)            original file
  ------- SEARCH ──┐         "a\nb\nc\n"
   b               │   locate(b) ─▶ [2,4)   exact, then line-trimmed fallback
  =======          │         │
   B               │         ▼
  +++++++ REPLACE ─┘   result = orig[last:matchStart] + REPLACE ; last = matchEnd
                              │
                   repeat per block (in file order) ; on isFinal append orig[last:]
                              ▼
                       new file content  →  write back to disk (sandboxed)
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | Canonical types copied from the shared catalog: `ContentBlock`, `ToolSchema`, the `Tool` interface |
| `diff.go` | The engine — `constructNewFileContent(diff, original, isFinal)`, marker matching, exact + line-trimmed locate, partial-marker trimming |
| `edit.go` | Sandboxed disk apply (`safeJoin`, `writeFile`, `applyDiffToFile`) + the `write_to_file` and `replace_in_file` tools |
| `main.go` | Offline demo: seed a file, apply a two-block diff, then replay it streamed |
| `diff_test.go` | 8 tests, fully offline (uses `t.TempDir`) |

---

## Try it / 动手试一试

Tests need no network and no API key / 测试无需联网、无需 API key：

```bash
cd agents/s07-file-edit-diff
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Watch a diff get applied (and streamed) with the offline demo /
用离线 demo 观察 diff 的应用与流式过程：

```bash
go run .
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt) —
this demo is deterministic (no LLM), so it reproduces byte-for-byte.

预期输出形态见该文件；本 demo 无 LLM，可逐字复现。

---

## Deliberately omitted / 故意省略

cline's `diff.ts` also ships a v2 state-machine constructor with non-standard-line
recovery (`tryFixSearchBlock` etc.), a block-anchor fallback for 3+ line blocks,
out-of-order replacement handling, legacy `<<<<<<<` / `>>>>>>>` markers, and
flexible marker lengths via regex. s07 teaches the v1 streaming **shape** plus the
two most load-bearing matchers (exact + line-trimmed); the fuzzier heuristics are
summarized, not ported, to stay focused.

cline 的 `diff.ts` 还有 v2 状态机构造器（含非标行修复）、3+ 行块的锚点退化匹配、
乱序替换、旧版标记、正则弹性标记长度等。s07 只教 v1 的流式**骨架**加两个最关键的
匹配器（精确 + 逐行去空白），其余模糊启发式只做概述。

See `docs/en/s07-file-edit-diff.md` (and `docs/zh/...`) for the full six-section
walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s07-file-edit-diff.md`。
