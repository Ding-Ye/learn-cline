import Link from "next/link";
import { notFound } from "next/navigation";
import { CURRICULUM, chapterTitle, type Locale } from "@/lib/curriculum";

export default async function Landing({
  params,
}: {
  params: Promise<{ locale: string }>;
}) {
  const { locale } = await params;
  if (locale !== "zh" && locale !== "en") notFound();
  const l = locale as Locale;

  const intro = l === "zh" ? INTRO_ZH : INTRO_EN;
  const ctaLabel = l === "zh" ? "从 s01 开始 →" : "Start at s01 →";

  return (
    <article className="prose-doc">
      <h1>learn-cline</h1>
      <p className="text-[var(--fg-muted)]">
        {l === "zh"
          ? "用 Go 从零渐进重写 cline 的自主编码智能体，每节末尾对照上游 TypeScript 源码。"
          : "A Go re-implementation of cline's autonomous coding agent, built from scratch — each chapter ends with the upstream TypeScript source."}
      </p>

      {intro.map((p, i) => (
        <p key={i}>{p}</p>
      ))}

      <p>
        <Link
          href={`/${l}/s/s01-minimum-agent-loop`}
          className="inline-block mt-2 px-4 py-2 rounded border border-[var(--accent-soft)] hover:border-[var(--accent)]"
        >
          {ctaLabel}
        </Link>
      </p>

      <h2>{l === "zh" ? "课程" : "Curriculum"}</h2>
      <ul>
        {CURRICULUM.map((c) => (
          <li key={c.slug}>
            <span className="font-mono text-[var(--fg-muted)] mr-2">
              {c.num}
            </span>
            {c.available ? (
              <Link href={`/${l}/s/${c.slug}`}>{chapterTitle(c, l)}</Link>
            ) : (
              <span className="text-[var(--fg-muted)]">
                {chapterTitle(c, l)}{" "}
                <span className="text-xs">
                  ({l === "zh" ? "未发布" : "not yet"})
                </span>
              </span>
            )}
          </li>
        ))}
      </ul>
    </article>
  );
}

const INTRO_ZH = [
  "这个仓库的目标不是教你「用」 cline，而是教你「它的智能体内核怎么从零长出来」。cline 是一个自主编码智能体——你给它一个任务，它读代码、改文件、跑命令，每一步都经过你的审批。它的核心其实很简单：一个围绕 LLM 的循环。",
  "每一节加一个机制，用 Go 写一份能跑的精简实现：任务循环（s01）、流式消息解析器（s02）、工具注册与执行（s03）、人类在环审批（s04）、供应商流式抽象（s05）、模块化系统提示词（s06）、文件差异编辑（s07）、上下文窗口管理（s08）、MCP 外部工具（s09）、影子 Git 检查点（s10）。看完十节，cline 不再是一团黑魔法。",
  "Go 实现是教学骨架，cline 上游是 TypeScript 生产实现（`apps/vscode/src/core/`）。每节末尾的「上游源码阅读」把两边对照起来——你能从几百行的 mini 版，顺着指针读进真实的生产代码。",
];

const INTRO_EN = [
  "The goal of this repo is not to teach you to *use* cline — it is to teach you how its agent core grows from scratch. cline is an autonomous coding agent: you give it a task, and it reads code, edits files, and runs commands, with your approval at every step. Its core is deceptively simple — a loop around an LLM.",
  "Each chapter adds one mechanism as a small, runnable Go re-implementation: the task loop (s01), the streaming message parser (s02), tool registry & execution (s03), human-in-the-loop approval (s04), the provider streaming abstraction (s05), the modular system prompt (s06), diff-based file editing (s07), context-window management (s08), MCP external tools (s09), and shadow-git checkpoints (s10). After ten chapters, cline stops being black magic.",
  "Go is the teaching skeleton; the upstream TypeScript is the production implementation (`apps/vscode/src/core/`). The 'Upstream Source Reading' section at the end of every chapter bridges them — you can follow the pointers from the few-hundred-line mini version straight into the real production code.",
];
