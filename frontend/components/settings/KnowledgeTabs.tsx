"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
export function KnowledgeTabs() {
  const pathname = usePathname();
  return <nav className="knowledge-tabs" aria-label="Knowledge sections">
    <Link href="/knowledge" aria-current={pathname === "/knowledge" ? "page" : undefined}>Sources & search</Link>
    <Link href="/knowledge/files" aria-current={pathname === "/knowledge/files" ? "page" : undefined}>Files & outputs</Link>
  </nav>;
}
