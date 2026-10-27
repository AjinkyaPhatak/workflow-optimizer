"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { assistantApi } from "@/lib/api/assistant";
import { workflowApi } from "@/lib/api";
import { ApiError } from "@/lib/api/client";
import { AssistantHome } from "@/components/assistant/AssistantHome";
import { TopBar } from "@/components/ui/TopBar";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { WorkflowEditor } from "./WorkflowEditor";

export function WorkflowEditorRoute({ workflowId }: { workflowId: string }) {
  const [mode, setMode] = useState<"loading" | "compiler" | "legacy" | "missing">("loading");
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let live = true;
    assistantApi.workflow(workflowId).then(() => { if (live) setMode("compiler"); }).catch(async err => {
      if (!live) return;
      if (err instanceof ApiError && err.status === 404) {
        try { await workflowApi.get(workflowId); if (live) setMode("legacy"); }
        catch (failure) { if (live) { setError(failure); setMode("missing"); } }
      } else { setError(err); setMode("missing"); }
    });
    return () => { live = false; };
  }, [workflowId]);
  if (mode === "loading") return <div className="page-loading">Loading workflow…</div>;
  if (mode === "legacy") return <WorkflowEditor workflowId={workflowId} />;
  if (mode === "compiler") return <><TopBar /><AssistantHome workflowId={workflowId} /></>;
  return <><TopBar /><main className="dashboard stack"><h1>This workflow is unavailable</h1><ErrorBanner error={error} /><Link className="button primary" href="/home">Start a new workflow</Link></main></>;
}
