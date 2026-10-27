"use client";

import { Component, type ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export function cleanAiText(text: string): string {
  return text.replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, "")
    .replace(/<[^>]+>/g, "").replace(/!\[([^\]]*)\]\([^)]+\)/g, "$1")
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, "$1 ($2)")
    .replace(/^ {0,3}#{1,6}\s+/gm, "").replace(/`{1,3}/g, "")
    .replace(/\*\*([^*]+)\*\*/g, "$1").replace(/__([^_]+)__/g, "$1");
}
class MarkdownBoundary extends Component<{ text: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() { return this.state.failed ? <div className="ai-plain">{cleanAiText(this.props.text)}</div> : this.props.children; }
}
export function AiMarkdown({ text, className = "" }: { text: string; className?: string }) {
  return <div className={"ai-markdown " + className}>
    <MarkdownBoundary key={text} text={text}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml components={{
        a: ({ children, href }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
        img: ({ alt }) => <span>{alt}</span>,
      }}>{text}</ReactMarkdown>
    </MarkdownBoundary>
  </div>;
}
