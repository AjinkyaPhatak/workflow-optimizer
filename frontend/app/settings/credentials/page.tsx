"use client";

import { Credentials } from "@/components/settings/Credentials";
import { TopBar } from "@/components/ui/TopBar";
import { useCredentials } from "@/features/credentials/useCredentials";
import { RequireAuth } from "@/lib/auth/RequireAuth";

function Page() {
  const state = useCredentials();
  return (
    <>
      <TopBar />
      <Credentials state={state} />
    </>
  );
}

export default function CredentialsPage() {
  return (
    <RequireAuth>
      <Page />
    </RequireAuth>
  );
}
