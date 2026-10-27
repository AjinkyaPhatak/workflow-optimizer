// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Knowledge } from "@/components/settings/Knowledge";
import type { KnowledgeState } from "@/features/knowledge/useKnowledge";
import type { KnowledgeBase, KnowledgeBaseDetail, KnowledgeDocument } from "@/types/api";

afterEach(cleanup);

const base = (over: Partial<KnowledgeBase> = {}): KnowledgeBase => ({
  id: "kb1", workspace_id: "ws", name: "Support", description: "Policies", credential_id: "c1", embedding_model: "text-embedding-3-small",
  documents: 2, chunks: 5, created_at: "", updated_at: "", ...over,
});
const doc = (over: Partial<KnowledgeDocument> = {}): KnowledgeDocument => ({
  id: "d1", title: "Refund policy", source: "text", file_id: null, status: "READY", chunks: 3, chars: 1200, created_at: "", updated_at: "", ...over,
});

function state(over: Partial<KnowledgeState> = {}): KnowledgeState {
  return {
    loading: false, error: null, workspaces: [{ id: "ws", name: "Main", role: "member" }], workspace: { id: "ws", name: "Main", role: "member" },
    setWorkspaceId: vi.fn(), bases: [], credentials: [], files: [], selected: null, select: vi.fn(), busy: null,
    create: vi.fn().mockResolvedValue(true), removeBase: vi.fn(), addText: vi.fn().mockResolvedValue(true), addURL: vi.fn().mockResolvedValue(true), copyLocal: vi.fn(), addFiles: vi.fn(),
    uploadAndAdd: vi.fn(), removeDocument: vi.fn(), reindex: vi.fn(), search: vi.fn().mockResolvedValue([]), ...over,
  };
}

const detail = (items: KnowledgeDocument[]): KnowledgeBaseDetail => ({ ...base(), items });

describe("Knowledge page", () => {
  it("offers local document search without requiring an API key", () => {
    render(<Knowledge state={state()} />);
    expect(screen.getByTestId("create-base").textContent).toContain("Local search");
    expect((screen.getByRole("button", { name: "Create" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("creates a base with the chosen credential", async () => {
    const s = state({ credentials: [{ id: "c1", workspace_id: "ws", name: "OpenAI", provider: "openai", credential_type: "api_key", created_at: "", updated_at: "" }] });
    render(<Knowledge state={s} />);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Support " } });
    fireEvent.change(screen.getByLabelText("Document search"), { target: { value: "c1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(s.create).toHaveBeenCalledWith("Support", "", "c1");
    await waitFor(() => expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe(""));
  });

  it("lists bases and shows document states with retry", () => {
    const s = state({
      bases: [base()],
      selected: detail([doc(), doc({ id: "d2", title: "Old", status: "FAILED", error: "OpenAI rejected the API key" }), doc({ id: "d3", status: "INDEXING", source: "file" })]),
    });
    render(<Knowledge state={s} confirm={() => true} />);
    fireEvent.click(screen.getByTestId("base-kb1"));
    expect(s.select).toHaveBeenCalledWith("kb1");
    expect(within(screen.getByTestId("doc-d1")).getByTestId("doc-status").textContent).toContain("Ready");
    const failed = screen.getByTestId("doc-d2");
    expect(failed.textContent).toContain("OpenAI rejected the API key");
    fireEvent.click(within(failed).getByRole("button", { name: "Retry" }));
    expect(s.reindex).toHaveBeenCalledWith("d2");
    expect(within(screen.getByTestId("doc-d3")).getByTestId("doc-status").textContent).toContain("Indexing");
    fireEvent.click(within(screen.getByTestId("doc-d1")).getByRole("button", { name: "Remove" }));
    expect(s.removeDocument).toHaveBeenCalledWith("d1");
  });

  it("adds text and files, and tries a search", async () => {
    const s = state({ bases: [base()], selected: detail([]), files: [{ id: "f1", workspace_id: "ws", name: "faq.pdf", content_type: "application/pdf", size: 10, created_at: "" }],
      search: vi.fn().mockResolvedValue([{ document_id: "d1", document_title: "Refund policy", text: "Five business days.", score: 0.82 }]) });
    render(<Knowledge state={s} />);
    fireEvent.change(screen.getByLabelText("Text"), { target: { value: "Refunds take five days." } });
    fireEvent.click(screen.getByRole("button", { name: "Add text" }));
    expect(s.addText).toHaveBeenCalledWith("", "Refunds take five days.");
    fireEvent.change(screen.getByLabelText("File to add"), { target: { value: "f1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add file" }));
    expect(s.addFiles).toHaveBeenCalledWith(["f1"]);
    fireEvent.change(screen.getByLabelText("Search question"), { target: { value: "how long are refunds" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByTestId("try-search").textContent).toContain("similarity 0.82"));
    expect(s.search).toHaveBeenCalledWith("how long are refunds");
  });

  it("lets viewers only search", () => {
    render(<Knowledge state={state({ bases: [base()], selected: detail([doc()]), workspace: { id: "ws", name: "Main", role: "viewer" } })} />);
    expect(screen.queryByTestId("create-base")).toBeNull();
    expect(screen.queryByTestId("add-documents")).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
    expect(screen.getByTestId("try-search")).toBeTruthy();
  });
});
