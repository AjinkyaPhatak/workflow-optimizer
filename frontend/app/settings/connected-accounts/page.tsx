"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useMemo } from "react";
import { ConnectedAccounts } from "@/components/settings/ConnectedAccounts";
import { TopBar } from "@/components/ui/TopBar";
import { useConnectedAccounts } from "@/features/connected-accounts/useConnectedAccounts";
import { RequireAuth } from "@/lib/auth/RequireAuth";
import { callbackOutcome } from "@/lib/connectedAccounts";

function Page() {
  // The OAuth callback redirects here with ?connected=... or ?error=<kind>:
  // an outcome only, never a token or code.
  const params = useSearchParams();
  const outcome = useMemo(() => callbackOutcome(new URLSearchParams(params.toString())), [params]);
  const state = useConnectedAccounts();
  return (
    <>
      <TopBar />
      <ConnectedAccounts state={state} outcome={outcome} />
    </>
  );
}

export default function ConnectedAccountsPage() {
  return (
    <RequireAuth>
      <Suspense fallback={<div className="page-loading">Loading…</div>}>
        <Page />
      </Suspense>
    </RequireAuth>
  );
}
