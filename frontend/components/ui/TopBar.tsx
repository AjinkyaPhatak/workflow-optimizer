"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useSession } from "@/lib/auth/session";

export function TopBar() {
  const router = useRouter();
  const { session, logout } = useSession();
  return (
    <header className="topbar">
      <Link href="/workflows" className="brand">Workflow Optimizer</Link>
      <span className="spacer" />
      {session && <span className="muted">{session.user.name}</span>}
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
