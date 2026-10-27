"use client";

import type { Compilation } from "@/types/automation";

export function CompilerIssues({ compilation, failure }: {
  compilation: Compilation; failure: string | null;
}) {
  if (!compilation.diagnostics.length) return null;
  return <section className="compiler-issues" aria-label="Workflow checks" aria-live="polite">
    {failure ? <p role="status">{failure}</p> : <>
      <div className="planning-progress" role="status"><span className="spinner" />Checking and fixing your workflow…</div>
      <p>I’ll handle the workflow details. I’ll only ask you if a fact or choice is missing.</p>
    </>}
  </section>;
}
