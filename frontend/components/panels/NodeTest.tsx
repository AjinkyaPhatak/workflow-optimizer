"use client";

// Phase E3: test the workflow up to the selected node and show its output.

import { FriendlyValue } from "@/components/run/FriendlyValue";
import { useTestRun } from "@/features/workflows/useTestRun";
import { friendlyError } from "@/lib/workflow/friendlyErrors";
import { useNodeTestResult, useTestRunStore } from "@/stores/test-run/store";
import { useShowDetails } from "@/stores/ui/details";

const LABEL = { COMPLETED: "Ran", FAILED: "Failed", SKIPPED: "Skipped (branch not taken)" } as const;

export function NodeTest({ nodeId }: { nodeId: string }) {
  const { run, running } = useTestRun();
  const until = useTestRunStore((s) => s.until);
  const result = useNodeTestResult(nodeId);
  const output = result?.output;
  const details = useShowDetails();
  return (
    <div className="section" data-testid="node-test">
      <h3 className="section-title">Test</h3>
      <div className="row">
        <button type="button" disabled={running} onClick={() => void run(nodeId)}>
          {running && until === nodeId ? "Testing…" : "Test up to this node"}
        </button>
        <span className="muted small">Tries this step and the steps before it, with your answers from the Run window. Nothing is saved.</span>
      </div>
      {result && (
        <div className="node-test-result" data-testid="node-test-result">
          <div className={`small node-test-status ${result.status.toLowerCase()}`}>
            {LABEL[result.status]}{result.duration_ms != null ? ` · ${result.duration_ms} ms` : ""}
          </div>
          {result.error && (details ? (
            <div className="field-error">{result.error.code}: {result.error.message}</div>
          ) : (
            <FriendlyFailure code={result.error.code} message={result.error.message} />
          ))}
          {output && Object.keys(output).length > 0 && (details ? (
            <pre className="json small" data-testid="node-test-output">{JSON.stringify(output, null, 2)}</pre>
          ) : (
            <div className="small" data-testid="node-test-output"><FriendlyValue value={output} /></div>
          ))}
          {result.status === "COMPLETED" && !output && <div className="muted small">Outputs are hidden on this server.</div>}
        </div>
      )}
    </div>
  );
}

/** A failed test in plain words (the code and raw message are shown with
 * technical details on). */
function FriendlyFailure({ code, message }: { code?: string; message?: string }) {
  const f = friendlyError(code, message);
  return (
    <div className="field-error">
      <b>{f.title}</b>
      {f.detail && <div>{f.detail}</div>}
      <div>{f.advice}</div>
    </div>
  );
}
