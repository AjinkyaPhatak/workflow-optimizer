import type { Binding, Plan, Step } from "../types/automation";
const literal = (name: string, value: unknown): Binding => ({ name, source: "literal", ref: "", value_json: JSON.stringify(value) });
const ref = (name: string, value: string): Binding => ({ name, source: "step", ref: value, value_json: "" });
function step(id: string, name: string, tool: string, inputs: Binding[]): Step {
  return { id, name, kind: "tool", tool, inputs, children: [], otherwise: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" };
}
export const FIXTURE_PLAN: Plan = {
  schema_version: 2, name: "Nifty 50 daily report", summary: "I’ll check the published data, create a CSV, calculate the statistics, and send the results to Phatak on WhatsApp.",
  mode: "once", budget: { max_actions: 30, max_tokens: 50000, max_active_seconds: 600 }, questions: [],
  steps: [
    step("research", "Check the published Nifty 50 data", "web.search", [literal("query", "Nifty 50 gainers and losers with dated published sources")]),
    step("table", "Read the actual figures", "table.extract", [ref("text", "research.text"), literal("instructions", "Extract all 50 fixture rows with symbol, change_pct, trading_date, source_url. Keep real values.")]),
    step("csv", "Create the CSV", "table.csv", [ref("rows", "table.rows"), literal("filename", "nifty50.csv")]),
    step("statistics", "Calculate descriptive statistics", "analysis.describe", [ref("rows", "table.rows")]),
    step("send", "Send the CSV and statistics to Phatak", "whatsapp.send", [literal("recipient", "Phatak"), ref("text", "statistics.text"), ref("file_id", "csv.artifact.id")]),
  ],
};
export const FIXTURE_ROWS = Array.from({ length: 50 }, (_, i) => ["FIXTURE" + String(i + 1).padStart(2, "0"), ((i - 25) / 10).toFixed(1), "2026-10-26", "https://example.org/nifty-fixture"]);
export function mockResponse(body: { model?: string; tools?: unknown[]; input?: string; text?: { format?: { name?: string } } }) {
  const format = body.text?.format?.name;
  let plan = FIXTURE_PLAN;
  if (format === "workflow_plan" && body.input) {
    const request = JSON.parse(body.input) as { repair?: boolean; previous?: Plan };
    if (request.repair && request.previous?.name === "Average rule demo") {
      plan = structuredClone(request.previous);
      plan.steps[0].condition!.op = "greater";
    }
  }
  const text = format === "workflow_plan" ? JSON.stringify(plan) :
    format === "source_table" ? JSON.stringify({ columns: ["symbol", "change_pct", "trading_date", "source_url"], rows: FIXTURE_ROWS, notes: "Deterministic test fixture, not live market data." }) :
      "Published test fixture for the 2026-10-26 session. Source: https://example.org/nifty-fixture. This fixture covers 50 named records.";
  return {
    id: "resp_fixture", object: "response", status: "completed", model: body.model, created_at: 1793030400,
    output: [{ type: "message", id: "msg_fixture", role: "assistant", status: "completed", content: [{ type: "output_text", text, annotations: [] }] }],
    usage: { input_tokens: 7, output_tokens: 5, total_tokens: 12 },
  };
}
