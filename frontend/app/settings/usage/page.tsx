"use client";

import { UsagePage } from "@/components/settings/Usage";
import { TopBar } from "@/components/ui/TopBar";
import { RequireAuth } from "@/lib/auth/RequireAuth";

export default function UsageRoute() {
  return (
    <RequireAuth>
      <TopBar />
      <UsagePage />
    </RequireAuth>
  );
}
