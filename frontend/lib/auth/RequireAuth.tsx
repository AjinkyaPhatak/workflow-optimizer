"use client";

import { useRouter } from "next/navigation";
import { useEffect, type ReactNode } from "react";
import { useSession } from "./session";

/** Renders children only with a session; otherwise redirects to /login. The
 * backend still authorizes every request: this guard is navigation only. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { session, hydrated, hydrate } = useSession();

  useEffect(() => {
    if (!hydrated) hydrate();
  }, [hydrated, hydrate]);

  useEffect(() => {
    if (hydrated && !session) router.replace("/login");
  }, [hydrated, session, router]);

  if (!hydrated || !session) return <div className="page-loading">Loading…</div>;
  return <>{children}</>;
}
