// The locked curriculum from the plan. SessionNav and the landing page both
// read from this single source of truth. Slugs match docs/{zh,en}/<slug>.md.
//
// "available: false" means the chapter exists in the curriculum but its
// docs aren't written yet — the link will render but go to a placeholder.

export type ChapterMeta = {
  slug: string;
  num: string; // "s01", "s02", "s_full"
  title: { zh: string; en: string };
  available: boolean;
};

export const CURRICULUM: ChapterMeta[] = [
  {
    slug: "multi-model",
    num: "M",
    title: {
      zh: "多模型接入指南（DeepSeek / Qwen / 自托管 …）",
      en: "Multi-model guide (DeepSeek / Qwen / self-hosted …)",
    },
    available: true,
  },
  {
    slug: "s01-minimum-agent-loop",
    num: "s01",
    title: { zh: "最小智能体循环", en: "Minimum agent loop" },
    available: true,
  },
  {
    slug: "s02-streaming-message-parser",
    num: "s02",
    title: { zh: "流式消息解析器", en: "Streaming message parser" },
    available: true,
  },
  {
    slug: "s03-tool-registry-execution",
    num: "s03",
    title: { zh: "工具注册与执行", en: "Tool registry & execution" },
    available: true,
  },
  {
    slug: "s04-approval-gating",
    num: "s04",
    title: {
      zh: "人类在环审批",
      en: "Human-in-the-loop approval gating",
    },
    available: true,
  },
  {
    slug: "s05-provider-streaming",
    num: "s05",
    title: { zh: "供应商流式抽象", en: "Provider streaming abstraction" },
    available: true,
  },
  {
    slug: "s06-system-prompt",
    num: "s06",
    title: { zh: "模块化系统提示词", en: "Modular system prompt" },
    available: true,
  },
  {
    slug: "s07-file-edit-diff",
    num: "s07",
    title: { zh: "文件编辑与差异应用", en: "File edit & diff application" },
    available: true,
  },
  {
    slug: "s08-context-window-management",
    num: "s08",
    title: { zh: "上下文窗口管理", en: "Context window management" },
    available: true,
  },
  {
    slug: "s09-mcp-integration",
    num: "s09",
    title: { zh: "MCP 外部工具集成", en: "MCP integration" },
    available: true,
  },
  {
    slug: "s10-checkpoints-shadow-git",
    num: "s10",
    title: { zh: "影子 Git 检查点", en: "Checkpoints via shadow git" },
    available: true,
  },
  {
    slug: "s_full-integration",
    num: "s_full",
    title: { zh: "集成全貌", en: "Full integration" },
    available: true,
  },
  {
    slug: "appendix-a-approval-safety-model",
    num: "A",
    title: {
      zh: "附录 A · 审批安全模型",
      en: "Appendix A · Approval safety model",
    },
    available: true,
  },
  {
    slug: "appendix-b-upstream-map",
    num: "B",
    title: {
      zh: "附录 B · 上游映射",
      en: "Appendix B · Upstream map",
    },
    available: true,
  },
];

export type Locale = "zh" | "en";

export function chapterTitle(c: ChapterMeta, locale: Locale): string {
  return c.title[locale];
}
