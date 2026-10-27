"use client";

import { Library } from "@/components/library/Library";
import { TopBar } from "@/components/ui/TopBar";
import { useFiles } from "@/features/files/useFiles";
import { useKnowledge } from "@/features/knowledge/useKnowledge";
import { RequireAuth } from "@/lib/auth/RequireAuth";

function Page() {
  const files = useFiles();
  const kn = useKnowledge();
  return (
    <>
      <TopBar />
      <Library files={files} kn={kn} />
    </>
  );
}

export default function LibraryPage() {
  return (
    <RequireAuth>
      <Page />
    </RequireAuth>
  );
}