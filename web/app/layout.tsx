import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "learn-cline",
  description:
    "Learn how cline's autonomous coding agent really works by re-implementing its core in Go, chapter by chapter — task loop, streaming parser, tools, approval, providers, MCP, and checkpoints.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="zh">
      <body>{children}</body>
    </html>
  );
}
