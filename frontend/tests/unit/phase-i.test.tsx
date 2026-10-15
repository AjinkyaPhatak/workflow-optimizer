// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FriendlyValue } from "@/components/run/FriendlyValue";
import { preview, resultValue, runSentence } from "@/components/run/RunHistory";
import { repeatChoices, repeatLabel } from "@/components/panels/RepeatSettings";
import { nodeOutputs } from "@/lib/workflow/ports";
import { timeAgo, tookLabel } from "@/lib/time";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { friendlyError } from "@/lib/workflow/friendlyErrors";
import { questionLabel, runFields, runInput } from "@/lib/workflow/runFields";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { Execution, NodeExecution, WorkflowDefinition } from "@/types/api";
import { catalog } from "./fixtures";

// The run hook is replaced so the window can be shown in any state.
const runState: { current: Record<string, unknown> } = { current: {} };
const runFn = vi.fn();
vi.mock("@/features/workflows/useRunWorkflow", () => ({
  useRunWorkflow: () => ({ execution: null, nodes: [], running: false, error: null, phase: "idle", blocked: null, busy: false, run: runFn, ...runState.current }),
}));
const { RunDialog } = await import("@/components/run/RunDialog");

afterEach(() => {
  cleanup();
  runFn.mockReset();
  runState.current = {};
});

function definition(): WorkflowDefinition {
  return {
    version: 1, settings: {},
    variables: [{ name: "tone", type: "string", default: "friendly" }, { name: "count", type: "number", default: null }],
    nodes: [
      { id: "in", type: "input", name: "Input", position: null, config: { field: "topic", field_type: "text" } },
      { id: "doc", type: "input", name: "Input 2", position: null, config: { field: "report", field_type: "file" } },
      { id: "ai", type: "llm", name: "Ask AI", position: null, config: { prompt: "About {{topic}} for {{input.audience}} in {{tone}} tone, see {{in.data}}, {{linked}}" } },
      { id: "out", type: "output", name: "Output", position: null, config: {} },
    ],
    edges: [{ id: "e", source: "in", source_port: "data", target: "ai", target_port: "var:linked" }],
  };
}

describe("run questions", () => {
  it("asks for Input fields, variables and names the settings read from the run", () => {
    const fields = runFields(definition());
    expect(fields.map((f) => [f.name, f.kind, f.required])).toEqual([
      ["topic", "text", true], ["report", "file", true], ["tone", "text", false], ["count", "number", true], ["audience", "text", true],
    ]);
    const { input, missing } = runInput(fields, { topic: "DAGs", count: "3", report: "f-1", tone: "" });
    expect(input).toEqual({ topic: "DAGs", count: 3, report: "f-1" });
    expect(missing).toEqual(["audience"]);
    expect(questionLabel("customer_name")).toBe("Customer name");
  });
});

describe("plain errors", () => {
  it("explains codes and says what to do", () => {
    expect(friendlyError("RATE_LIMITED")).toMatchObject({ title: "The service asked us to slow down", action: "try_again" });
    expect(friendlyError("PERMISSION_DENIED", "Reconnect the Google account").detail).toBe("Reconnect the Google account");
    expect(friendlyError("SOMETHING_NEW")).toMatchObject({ title: "A step did not work", action: "show_step" });
    expect(friendlyError(undefined, "unresolved variable reference {{x}}").action).toBe("fill_inputs");
  });
});

describe("results", () => {
  it("shows text, tables and records, not JSON", () => {
    const { container, rerender } = render(<FriendlyValue value={"Line one\nLine two"} />);
    expect(container.querySelector(".friendly-text-body")!.textContent).toBe("Line one\nLine two");
    rerender(<FriendlyValue value={[{ subject: "Hi", unread: true }, { subject: "Bye", unread: false }]} />);
    expect([...container.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Subject", "Unread"]);
    expect(container.querySelectorAll("tbody tr")[0].textContent).toBe("HiYes");
    rerender(<FriendlyValue value={{ message_id: "m1", count: 2 }} />);
    expect(container.querySelector("dl")!.textContent).toBe("Message id" + "m1" + "Count2");
    expect(container.querySelector("pre")).toBeNull();
  });
});

describe("the Run window", () => {
  const session = { workflow: { id: "wf-1", name: "Digest" }, catalog, workspaceId: "ws" } as unknown as EditorSession;
  const Editor = ({ children }: { children: ReactNode }) => (
    <ReactFlowProvider><EditorContext.Provider value={session}>{children}</EditorContext.Provider></ReactFlowProvider>
  );
  beforeEach(() => {
    const def = definition();
    def.nodes = def.nodes.filter((n) => n.id !== "doc");
    def.variables = [];
    useEditorStore.getState().load(def);
  });

  it("asks the questions and runs with the answers", () => {
    render(<Editor><RunDialog open onClose={() => {}} workflowId="wf-1" /></Editor>);
    fireEvent.click(screen.getByTestId("run-start"));
    expect(runFn).not.toHaveBeenCalled();
    // {{tone}} is not a declared variable here, so it is asked for too.
    expect(screen.getByRole("alert").textContent).toBe("Fill in Topic, Audience, Tone.");
    fireEvent.change(screen.getByLabelText(/^Topic/), { target: { value: "DAGs" } });
    fireEvent.change(screen.getByLabelText(/^Audience/), { target: { value: "students" } });
    fireEvent.change(screen.getByLabelText(/^Tone/), { target: { value: "warm" } });
    fireEvent.click(screen.getByTestId("run-start"));
    expect(runFn).toHaveBeenCalledWith({ topic: "DAGs", audience: "students", tone: "warm" });
  });

  it("shows the result, or what went wrong in plain words", () => {
    const done = { id: "x", status: "COMPLETED", output: { out: { value: "A short post about DAGs." } }, started_at: "2026-10-15T10:00:00Z", completed_at: "2026-10-15T10:00:02.5Z", created_at: "" } as unknown as Execution;
    runState.current = { execution: done };
    const { rerender } = render(<Editor><RunDialog open onClose={() => {}} workflowId="wf-1" /></Editor>);
    expect(screen.getByTestId("run-success").textContent).toContain("✓ Done in 2.5 s");
    expect(screen.getByTestId("run-success").textContent).toContain("A short post about DAGs.");

    const failed = { ...done, status: "FAILED", output: null, error: { code: "NODE_FAILED", message: "node failed", node_id: "ai", retryable: false } } as unknown as Execution;
    const nodes = [{ node_id: "ai", status: "FAILED", error: { code: "CREDENTIAL_INVALID", message: "credential_id is required" } }] as unknown as NodeExecution[];
    runState.current = { execution: failed, nodes };
    rerender(<Editor><RunDialog open onClose={() => {}} workflowId="wf-1" /></Editor>);
    const failure = screen.getByTestId("run-failure");
    expect(failure.textContent).toContain("A step needs an account or key (Ask AI)");
    expect(failure.textContent).toContain("Choose one on the step's card.");
    expect(failure.textContent).not.toContain("CREDENTIAL_INVALID");
    expect(screen.getByRole("button", { name: "Show me" })).toBeTruthy();
  });

  it("says when the workflow has problems to fix first", () => {
    runState.current = { blocked: "problems" };
    useEditorStore.getState().setValidation({ valid: false, warnings: [], errors: [{ code: "DISCONNECTED_NODE", message: "node cannot reach an exit", node_id: "ai" }] });
    render(<Editor><RunDialog open onClose={() => {}} workflowId="wf-1" /></Editor>);
    expect(screen.getByTestId("run-problems").textContent).toContain("Some steps need attention");
  });
});

describe("run history", () => {
  it("says what happened, when and how long, with a preview", () => {
    expect(resultValue({ out: { value: "Hello" } })).toBe("Hello");
    expect(resultValue({ a: { value: 1 }, b: { value: 2 } })).toEqual({ a: { value: 1 }, b: { value: 2 } });
    expect(preview("x ".repeat(80)).length).toBe(108);
    expect(preview([1, 2, 3])).toBe("3 items");
    expect(runSentence({ status: "COMPLETED", error: null, attempt: 2 }).text).toBe("Worked (after 2 tries)");
    expect(runSentence({ status: "FAILED", error: { code: "RATE_LIMITED", message: "", retryable: true }, attempt: 1 }).text)
      .toBe("Did not work: the service asked us to slow down");
    const now = Date.parse("2026-10-15T12:00:00Z");
    expect(timeAgo("2026-10-15T11:59:50Z", now)).toBe("just now");
    expect(timeAgo("2026-10-15T11:55:00Z", now)).toBe("5 min ago");
    expect(timeAgo("2026-10-14T12:00:00Z", now)).toBe("1 day ago");
    expect(tookLabel(2500)).toBe("2.5 s");
    expect(tookLabel(185_000)).toBe("3 min 5 s");
  });
});

describe("repeat for each item", () => {
  it("offers a step's inputs and {{names}}, and makes its outputs lists", () => {
    const llm = catalog.get("llm")!;
    const n = { id: "ai", type: "llm", name: "Ask AI", position: null, config: { prompt: "Summarize {{email}}" } };
    const choices = repeatChoices(n, llm, { nodes: [n], edges: [] });
    expect(choices.map((c) => c.label)).toEqual(["{{email}}", "Prompt", "System"]);
    expect(choices[0].value).toBe("var:email");
    expect(repeatLabel("var:email")).toBe("{{email}}");
    expect(nodeOutputs(llm, { settings: { repeat_over: "var:email" } }).map((p) => p.type)).toEqual(["array", "array"]);
    expect(nodeOutputs(llm, {}).map((p) => p.type)).toEqual(["string", "json"]);
  });
});
