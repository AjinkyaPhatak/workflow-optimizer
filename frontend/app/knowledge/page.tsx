"use client";

import { Knowledge } from "@/components/settings/Knowledge";
import { TopBar } from "@/components/ui/TopBar";
import { useKnowledge } from "@/features/knowledge/useKnowledge";
import { RequireAuth } from "@/lib/auth/RequireAuth";

function Page() {
  const state = useKnowledge();
  return (
    <>
      <TopBar />
      <Knowledge state={state} />
    </>
  );
}

export default function KnowledgePage() {
  return (
    <RequireAuth>
      <Page />
    </RequireAuth>
  );
}
