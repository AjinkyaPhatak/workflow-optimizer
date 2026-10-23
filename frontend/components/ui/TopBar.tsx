"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useSession } from "@/lib/auth/session";
import { BrandMark } from "./BrandMark";

export function TopBar() {
  const router = useRouter();
  const { session, logout } = useSession();
  return (
    <header className="topbar">
      <Link href="/workflows" className="brand"><BrandMark size={22} />Workflow Optimizer</Link>
      <span className="spacer" />
      <Link href="/workflows" className="topbar-link">Workflows</Link>
      <Link href="/templates" className="topbar-link">Templates</Link>
      <Link href="/knowledge" className="topbar-link">Knowledge</Link>
      <Link href="/files" className="topbar-link">Files</Link>
      <Link href="/settings/credentials" className="topbar-link">Credentials</Link>
      <Link href="/settings/connected-accounts" className="topbar-link">Connected accounts</Link>
      <Link href="/settings/usage" className="topbar-link">Usage</Link>
      {session && <Link href="/settings/account" className="topbar-link" title="Account settings" data-testid="account-link">{session.user.name}</Link>}
      <button
        onClick={() => {
          logout();
          router.replace("/login");
        }}
      >
        Log out
      </button>
    </header>
  );
}
