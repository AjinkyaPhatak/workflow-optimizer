"use client";

import { Files } from "@/components/settings/Files";
import { TopBar } from "@/components/ui/TopBar";
import { useFiles } from "@/features/files/useFiles";
import { RequireAuth } from "@/lib/auth/RequireAuth";

function Page() {
  const state = useFiles();
  return (
    <>
      <TopBar />
      <Files state={state} />
    </>
  );
}

export default function FilesPage() {
  return (
    <RequireAuth>
      <Page />
    </RequireAuth>
  );
}
