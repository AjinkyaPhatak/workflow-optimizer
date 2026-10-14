"use client";

// Phase E3: test the workflow up to the selected node and show its output.

import { useTestRun } from "@/features/workflows/useTestRun";
import { useNodeTestResult, useTestRunStore } from "@/stores/test-run/store";

const LABEL = { COMPLETED: "Ran", FAILED: "Failed", SKIPPED: "Skipped (branch not taken)" } as const;

export function NodeTest({ nodeId }: { nodeId: string }) {
  const { run, running } = useTestRun();
  const until = useTestRunStore((s) => s.until);
  const result = useNodeTestResult(nodeId);
  const output = result?.output;
  return (
    <div className="section" data-testid="node-test">
      <h3 className="section-title">Test</h3>
      <div className="row">
        <button type="button" disabled={running} onClick={() => void run(nodeId)}>
          {running && until === nodeId ? "Testing…" : "Test up to this node"}
        </button>
        <span className="muted small">Runs this node and what it depends on, with the Run tab input. Nothing is saved.</span>
      </div>
      {result && (
        <div className="node-test-result" data-testid="node-test-result">
          <div className={`small node-test-status ${result.status.toLowerCase()}`}>
            {LABEL[result.status]}{result.duration_ms != null ? ` · ${result.duration_ms} ms` : ""}
          </div>
          {result.error && <div className="field-error">{result.error.code}: {result.error.message}</div>}
          {output && Object.keys(output).length > 0 && (
            <pre className="json small" data-testid="node-test-output">{JSON.stringify(output, null, 2)}</pre>
          )}
          {result.status === "COMPLETED" && !output && <div className="muted small">Outputs are hidden on this server.</div>}
        </div>
      )}
    </div>
  );
}
