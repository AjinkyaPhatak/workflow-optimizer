// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExecutionTimeline } from "@/components/executions/ExecutionTimeline";
import { JsonViewer } from "@/components/executions/JsonViewer";
import { NodeInspector } from "@/components/executions/NodeInspector";
import { buildTimeline } from "@/features/executions/timeline";
import { definition, ev, rateLimited, record } from "./debugger-fixtures";

afterEach(cleanup);

function Debugger({ rows }: { rows: ReturnType<typeof buildTimeline> }) {
  const [selected, setSelected] = useState<string | null>(null);
  const row = rows.find((r) => r.nodeId === selected) ?? null;
  return (
    <>
      <ExecutionTimeline rows={rows} selected={selected} onSelect={setSelected} />
      <NodeInspector key={row?.nodeId ?? "none"} row={row} />
    </>
  );
}

const retried = () =>
  buildTimeline(
    "COMPLETED",
    definition,
    [
      record("in", "COMPLETED"),
      record("llm_1", "FAILED", { error: rateLimited, usage: null, output: undefined }),
      record("llm_1", "COMPLETED", { attempt: 2 }),
      record("out", "COMPLETED"),
    ],
    [ev("NODE_FAILED", "llm_1"), ev("RETRY_SCHEDULED", "llm_1", { scope: "node", delay_ms: 1000 }), ev("RETRY_STARTED", "llm_1")],
  );

describe("execution debugger components", () => {
  it("renders statuses, attempts and the retry in the timeline", () => {
    render(<Debugger rows={retried()} />);
    expect(screen.getByTestId("timeline-in").dataset.status).toBe("COMPLETED");
    const llm = screen.getByTestId("timeline-llm_1");
    expect(llm.dataset.status).toBe("COMPLETED");
    expect(screen.getByTestId("attempt-llm_1-1").textContent).toContain("Attempt 1: FAILED · RATE_LIMITED → retry in 1.00 s");
    expect(screen.getByTestId("attempt-llm_1-2").textContent).toContain("Attempt 2: COMPLETED");
  });

  it("selecting a node opens the inspector with its details, usage and data", () => {
    render(<Debugger rows={retried()} />);
    expect(screen.getByText(/Select a node/)).toBeTruthy();
    fireEvent.click(within(screen.getByTestId("timeline-llm_1")).getByRole("button"));
    const inspector = screen.getByTestId("node-inspector");
    expect(within(inspector).getByRole("heading").textContent).toBe("Answer");
    expect(screen.getByTestId("inspector-status").textContent).toBe("COMPLETED");
    expect(screen.getByTestId("inspector-provider").textContent).toBe("openai");
    expect(screen.getByTestId("inspector-model").textContent).toBe("gpt-5-mini-2025");
    expect(screen.getByTestId("inspector-tokens").textContent).toMatch(/Input 812 · Output 231 · Total 1,043/);
    expect(screen.getByTestId("inspector-estimated-cost").textContent).toContain("(estimate)");
    expect(screen.getByTestId("json-output").textContent).toContain("An answer");
    // The failed first attempt, with its structured error.
    fireEvent.click(within(inspector).getByRole("tab", { name: "Attempt 1" }));
    expect(screen.getByTestId("inspector-status").textContent).toBe("FAILED");
    expect(screen.getByTestId("inspector-error").textContent).toContain("RATE_LIMITED");
    expect(screen.queryByTestId("inspector-tokens")).toBeNull();
  });

  it("a node that never ran says so", () => {
    const rows = buildTimeline("FAILED", definition, [record("in", "COMPLETED"), record("llm_1", "FAILED", { error: rateLimited })], []);
    render(<Debugger rows={rows} />);
    fireEvent.click(within(screen.getByTestId("timeline-out")).getByRole("button"));
    expect(screen.getByText("This node did not run.")).toBeTruthy();
    expect(screen.getByTestId("timeline-llm_1").textContent).toContain("RATE_LIMITED: provider rate limit exceeded");
  });

  it("JSON viewer collapses, shows long values in a scroll box, and copies", async () => {
    const writeText = vi.fn(async () => {});
    Object.assign(navigator, { clipboard: { writeText } });
    const value = { query: "Explain quantum computing", nested: { deep: { deeper: 1 } }, long: "x".repeat(400) };
    render(<JsonViewer label="Input" value={value} defaultDepth={2} />);
    const viewer = screen.getByTestId("json-input");
    expect(viewer.textContent).toContain('"Explain quantum computing"');
    expect(viewer.querySelector(".json-long")).toBeTruthy();
    // depth 2 is collapsed: "deeper" hidden until expanded
    expect(viewer.textContent).not.toContain("deeper");
    const toggles = within(viewer).getAllByRole("button", { expanded: false });
    fireEvent.click(toggles[0]);
    expect(viewer.textContent).toContain("deeper");
    const root = within(viewer).getAllByRole("button", { expanded: true })[0];
    fireEvent.click(root);
    expect(viewer.textContent).toContain("3 keys");
    await act(async () => {
      fireEvent.click(within(viewer).getByRole("button", { name: "Copy" }));
    });
    expect(writeText).toHaveBeenCalledWith(JSON.stringify(value, null, 2));
    expect(within(viewer).getByRole("button", { name: "Copied" })).toBeTruthy();
  });
});
