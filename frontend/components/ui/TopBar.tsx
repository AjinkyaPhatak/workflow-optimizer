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
      <Link href="/settings/connected-accounts" className="topbar-link">Connected accounts</Link>
      {session && <span className="muted small">{session.user.name}</span>}
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
