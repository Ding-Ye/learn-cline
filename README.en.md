# learn-cline

> A progressive Go re-implementation of [cline](https://github.com/cline/cline)'s autonomous coding agent core — every chapter pairs with a close reading of the upstream TypeScript source.

English · [简体中文](./README.md)

## What this is

cline is an autonomous coding agent: you give it a task, and it reads code, edits files, and runs commands, with your approval at every step. Its core is deceptively simple — **a loop around an LLM**.

This repo does not teach you to *use* cline; it teaches you how its agent core grows from scratch. Each chapter is a small, self-contained Go re-implementation that teaches exactly one of cline's mechanisms, and its "Upstream Source Reading" section maps your mini version onto the real upstream TypeScript production code (`apps/vscode/src/core/`) — so you can follow the pointers from a few hundred lines straight into the real engineering.

Every chapter is a **self-contained Go module** (`learn-cline/sNN`): no cross-chapter imports, runnable on its own with `go run`.

## Curriculum

| # | Chapter | Mechanism | Status |
|---|---------|-----------|--------|
| s01 | [Minimum agent loop](docs/en/s01-minimum-agent-loop.md) | request → response → tool → result → loop | ✅ |
| s02 | [Streaming message parser](docs/en/s02-streaming-message-parser.md) | incrementally parse XML tool calls from a growing string | ✅ |
| s03 | [Tool registry & execution](docs/en/s03-tool-registry-execution.md) | name-dispatched tool registry + `tool_result` wrapping | ✅ |
| s04 | [Human-in-the-loop approval gating](docs/en/s04-approval-gating.md) | approval gate + auto-approve allowlist | ✅ |
| s05 | [Provider streaming abstraction](docs/en/s05-provider-streaming.md) | parse the SSE stream + provider factory | ✅ |
| s06 | [Modular system prompt](docs/en/s06-system-prompt.md) | variant-driven prompt builder + tool-spec injection | ✅ |
| s07 | [File edit & diff application](docs/en/s07-file-edit-diff.md) | streaming application of SEARCH/REPLACE diff blocks | ✅ |
| s08 | [Context window management](docs/en/s08-context-window-management.md) | safe truncation under a token budget | ✅ |
| s09 | [MCP integration](docs/en/s09-mcp-integration.md) | stdio JSON-RPC client + remote-tool adapter | ✅ |
| s10 | [Checkpoints via shadow git](docs/en/s10-checkpoints-shadow-git.md) | per-step snapshot & restore via a shadow git repo | ✅ |
| s_full | Full integration | wire all mechanisms into one complete cline agent | ✅ |
| A | Appendix A · Approval safety model | human-in-the-loop + Plan/Act mode | ✅ |
| B | Appendix B · Upstream map | chapter-to-upstream-source cross-reference | ✅ |

## Quickstart

Run the minimum agent loop from chapter one:

```bash
cd agents/s01-minimum-agent-loop
go run . "build a TODO CLI"
```

s01 defaults to a scripted fake provider, so it runs offline. To wire in a real LLM, set the `ANTHROPIC_API_KEY` environment variable and pick a backend with `-provider` (the provider abstraction is introduced in s05).

## Web doc viewer

This repo ships a bilingual Next.js documentation viewer:

```bash
cd web
npm install
npm run dev
```

Open http://localhost:3000 to browse the chapter docs and upstream source-reading guides.

## Acknowledgements

- Upstream [cline/cline](https://github.com/cline/cline), Apache-2.0 — this repo is a pedagogical Go re-implementation of its agent core.
- Pedagogy inspired by [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/analysis_claude_code).

## License

[MIT](./LICENSE)
