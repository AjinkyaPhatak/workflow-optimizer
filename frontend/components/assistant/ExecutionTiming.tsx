"use client";
import { useEffect, useState } from "react";
import type { AutomationRun, Plan, Step } from "@/types/automation";

function seconds(steps: Step[]): number {
  return (steps ?? []).reduce((total, step) => {
    const children = seconds(step.children), other = seconds(step.otherwise);
    const work = step.kind === "tool" ? /^web\.(query|search)$/.test(step.tool) ? 45 : /^(ai\.|table.extract|node.llm)/.test(step.tool) ? 25 : step.tool.startsWith("analysis.") ? 10 : 3
      : step.kind === "wait" ? step.duration_seconds : step.kind === "branch" ? Math.max(children, other) : children;
    return total + work;
  }, 0);
}
export function ExecutionTiming({ plan, run }: { plan: Plan; run: AutomationRun | null }) {
  const [now, setNow] = useState(() => Date.now());
  const active = run && !["COMPLETED", "FAILED", "CANCELLED"].includes(run.status);
  useEffect(() => { if (!active) return; const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer); }, [active]);
  const work = plan.mode === "continuous" ? plan.steps.filter((step, i) => step.kind !== "wait" || i !== plan.steps.length - 1) : plan.steps;
  const estimate = Math.max(5, seconds(work));
  const end = active ? now : Date.parse(run?.updated_at ?? "");
  const elapsed = run ? Math.max(0, Math.floor((end - Date.parse(run.created_at)) / 1000)) : 0;
  return <p className="hint" role="status" style={{ margin: "0 0 6px" }}>
    {run && Number.isFinite(elapsed) ? `Elapsed ${Math.floor(elapsed / 60)}m ${elapsed % 60}s · ` : ""}
    Rough first-result estimate: {Math.max(5, Math.round(estimate / 2))}–{Math.round(estimate * 2)}s.
    {plan.mode === "continuous" && " Repeats until you stop it."}
  </p>;
}
