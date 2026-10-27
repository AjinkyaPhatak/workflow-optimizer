// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AiMarkdown, cleanAiText } from "@/components/ui/AiMarkdown";
import { ClarificationCard } from "@/components/assistant/ClarificationCard";
import { materializePlan } from "@/features/assistant/useAssistant";
import type { Plan, Question } from "@/types/automation";
import { CompilerIssues } from "@/components/assistant/CompilerIssues";
afterEach(cleanup);
const question: Question = { id: "q", prompt: "Which person?", kind: "choice", required: true, allow_other: true, options: [{ label: "Ajinkya", value: "a" }], example: "", min: null, max: null, step_id: "send", input: "recipient" };
describe("Assistant text and clarification", () => {
  it("shows automatic repair progress without asking the person to fix technical issues", () => {
    const plan = { steps: [{ id: "average_gate", name: "Email only when the average is above 150", children: [], otherwise: [] }] } as unknown as Plan;
    render(<CompilerIssues compilation={{ plan, ready: false, diagnostics: [{ step_id: "average_gate", code: "CONDITION", message: "Choose a supported comparison." }] }} failure={null} />);
    expect(screen.getByRole("status").textContent).toContain("Checking and fixing your workflow");
    expect(screen.queryByText("Choose a supported comparison.")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });
  it("renders Markdown tables and tolerates partial Markdown without executing HTML", () => {
    const view = render(<AiMarkdown text={"| Name | Amount |\n| --- | --- |\n| Ajinkya | 42 |\n\n<script>alert('x')</script>\n\n**unfinished"} />);
    expect(screen.getByRole("table")).toBeTruthy();
    expect(view.container.querySelector("script")).toBeNull();
    expect(view.container.textContent).toContain("unfinished");
    expect(cleanAiText("## Hello **Ajinkya**\n<script>bad()</script>")).toBe("Hello Ajinkya\n");
  });
  it("lets someone type another option and validates before proceeding", async () => {
    const answer = vi.fn().mockResolvedValue(undefined);
    render(<ClarificationCard question={question} onAnswer={answer} />);
    fireEvent.change(screen.getByLabelText("Which person?"), { target: { value: "__other" } });
    fireEvent.change(screen.getByLabelText("Your other answer"), { target: { value: "Another person" } });
    fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(answer).toHaveBeenCalledWith("Another person");
  });
  it("shows the email step and purpose before accepting guidance to update the workflow", async () => {
    const answer = vi.fn().mockResolvedValue(undefined);
    render(<ClarificationCard question={{ ...question, kind: "text", purpose: "intent", prompt: "What should the recipient learn from this email?",
      step_name: "Email qualifying Nifty gainers", field_label: "Message content", context: "The email only sends if a gainer rose by more than 0.7%.", options: [] }} onAnswer={answer} />);
    expect(screen.getByTestId("clarification-location").textContent).toContain("Email qualifying Nifty gainers");
    expect(screen.getByText("The email only sends if a gainer rose by more than 0.7%.")).toBeTruthy();
    const input = screen.getByLabelText("What should the recipient learn from this email?");
    expect(input.tagName).toBe("TEXTAREA");
    fireEvent.change(input, { target: { value: "Summarize the qualifying companies and include the source links." } });
    fireEvent.click(screen.getByRole("button", { name: "Update workflow" }));
    expect(answer).toHaveBeenCalledWith("Summarize the qualifying companies and include the source links.");
  });
  it("keeps the rest of a template when freezing answers for scheduling", () => {
    const plan = { schema_version: 2, questions: [{ ...question, id: "message_0" }], steps: [{ id: "message", inputs: [{ name: "text", source: "template", ref: "message", value_json: JSON.stringify("Hello [name]") }], children: [], otherwise: [] }] } as unknown as Plan;
    const frozen = materializePlan(plan, { message_0: "Ajinkya" });
    expect(frozen.steps[0].inputs[0].source).toBe("literal");
    expect(JSON.parse(frozen.steps[0].inputs[0].value_json)).toBe("Hello Ajinkya");
    expect(frozen.questions).toHaveLength(0);
  });
});
