"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { useSession } from "@/lib/auth/session";
import { BrandMark } from "./BrandMark";

/** The main sections, in the top bar. */
const SECTIONS = [
  { href: "/workflows", label: "Workflows" },
  { href: "/templates", label: "Templates" },
  { href: "/knowledge", label: "Knowledge" },
  { href: "/files", label: "Files" },
];

/** Settings, in the account menu. */
const SETTINGS = [
  { href: "/settings/account", label: "Account", hint: "Your name, password and workspace" },
  { href: "/settings/connected-accounts", label: "Connected accounts", hint: "Google, Notion, Discord, Airtable" },
  { href: "/settings/credentials", label: "AI keys", hint: "Gemini and other keys" },
  { href: "/settings/usage", label: "Usage", hint: "Runs, AI and storage" },
];

/** "Ada Lovelace" -> "AL" */
export function initials(name: string | undefined): string {
  const words = (name ?? "").trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  return (words[0][0] + (words.length > 1 ? words[words.length - 1][0] : "")).toUpperCase();
}

function active(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(href + "/");
}

export function TopBar() {
  const router = useRouter();
  const pathname = usePathname() ?? "";
  const { session, logout } = useSession();
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !menuRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);

  return (
    <header className="topbar">
      <Link href="/workflows" className="brand"><BrandMark size={22} />Workflow Optimizer</Link>
      <nav className="topbar-nav" aria-label="Main">
        {SECTIONS.map((s) => (
          <Link key={s.href} href={s.href} className={`topbar-link ${active(pathname, s.href) ? "active" : ""}`}
            aria-current={active(pathname, s.href) ? "page" : undefined}>{s.label}</Link>
        ))}
      </nav>
      <span className="spacer" />
      {session && (
        <div className="account-menu" ref={menuRef}>
          <button type="button" className={`account-button ${SETTINGS.some((s) => active(pathname, s.href)) ? "active" : ""}`}
            aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)} data-testid="account-link">
            <span className="avatar" aria-hidden>{initials(session.user.name)}</span>
            <span className="account-name">{session.user.name}</span>
            <span className="caret" aria-hidden>▾</span>
          </button>
          {open && (
            <div className="account-dropdown" role="menu">
              <div className="account-who">
                <b>{session.user.name}</b>
                <span className="small muted">{session.user.email}</span>
              </div>
              {SETTINGS.map((s) => (
                <Link key={s.href} href={s.href} role="menuitem" className={`account-item ${active(pathname, s.href) ? "active" : ""}`}
                  onClick={() => setOpen(false)}>
                  <span>{s.label}</span>
                  <span className="small muted">{s.hint}</span>
                </Link>
              ))}
              <button type="button" role="menuitem" className="account-item signout" onClick={() => {
                logout();
                router.replace("/login");
              }}>Log out</button>
            </div>
          )}
        </div>
      )}
    </header>
  );
}
