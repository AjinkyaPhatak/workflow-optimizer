// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Library } from "@/components/library/Library";
import type { FilesState } from "@/features/files/useFiles";
import type { KnowledgeState } from "@/features/knowledge/useKnowledge";
import { knowledgeApi } from "@/lib/api";
import type { KnowledgeBaseDetail, WorkspaceFile } from "@/types/api";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const workspace = { id: "ws", name: "Main", role: "member" as const };
const file: WorkspaceFile = { id: "f1", workspace_id: "ws", name: "faq.pdf", content_type: "application/pdf", size: 10, created_at: "2026-10-28" };
const base: KnowledgeBaseDetail = {
  id: "kb1", workspace_id: "ws", name: "Support", description: "Policies", credential_id: "c1",
  embedding_model: "text-embedding-3-small", documents: 1, chunks: 3, created_at: "", updated_at: "",
  items: [{ id: "d1", title: "Public FAQ", source: "url", file_id: null, status: "READY", chunks: 3, chars: 1200, created_at: "", updated_at: "" }],
};

function filesState(): FilesState {
  return { loading: false, error: null, workspaces: [workspace], workspace, setWorkspaceId: vi.fn(),
    files: [file], busy: null, upload: vi.fn(), remove: vi.fn(), download: vi.fn() };
}

function knowledgeState(over: Partial<KnowledgeState> = {}): KnowledgeState {
  return {
    loading: false, error: null, workspaces: [workspace], workspace, setWorkspaceId: vi.fn(),
    bases: [base], credentials: [], files: [file], selected: base, select: vi.fn(), busy: null,
    create: vi.fn().mockResolvedValue(true), removeBase: vi.fn(), addText: vi.fn().mockResolvedValue(true),
    addURL: vi.fn().mockResolvedValue(true), copyLocal: vi.fn(), addFiles: vi.fn(), uploadAndAdd: vi.fn(),
    removeDocument: vi.fn(), reindex: vi.fn(), search: vi.fn().mockResolvedValue([]), ...over,
  };
}

function openBase(kn = knowledgeState()) {
  render(<Library files={{ ...filesState(), workspace: kn.workspace }} kn={kn} />);
  fireEvent.click(screen.getByTestId("base-kb1"));
  return kn;
}

describe("integrated Library knowledge", () => {
  it("labels URL documents as Web page and copies hosted bases to local search", () => {
    const kn = openBase();
    expect(screen.getByTestId("doc-d1").textContent).toContain("Web page");
    fireEvent.click(screen.getByRole("button", { name: "Copy to local search" }));
    expect(kn.copyLocal).toHaveBeenCalledOnce();
  });

  it("disables copy while the hosted base is copying", () => {
    const kn = openBase(knowledgeState({ busy: "copy" }));
    const copy = screen.getByRole("button", { name: "Copying…" }) as HTMLButtonElement;
    expect(copy.disabled).toBe(true);
    fireEvent.click(copy);
    expect(kn.copyLocal).not.toHaveBeenCalled();
  });

  it("does not offer copy for local bases", () => {
    openBase(knowledgeState({ selected: { ...base, embedding_model: "intfloat/multilingual-e5-small" } }));
    expect(screen.queryByRole("button", { name: "Copy to local search" })).toBeNull();
    expect(screen.getByTestId("try-search")).toBeTruthy();
  });

  it("lets viewers search without offering knowledge mutations", () => {
    openBase(knowledgeState({ workspace: { ...workspace, role: "viewer" } }));
    expect(screen.queryByRole("button", { name: "Copy to local search" })).toBeNull();
    expect(screen.queryByTestId("add-documents")).toBeNull();
    expect(within(screen.getByTestId("base-detail")).queryByRole("button", { name: "Delete" })).toBeNull();
    expect(screen.getByTestId("try-search")).toBeTruthy();
  });

  it("keeps web, text and file ingestion and formatted search results", async () => {
    const kn = openBase(knowledgeState({ search: vi.fn().mockResolvedValue([
      { document_id: "d1", document_title: "Public FAQ", text: "**Five days.**", score: 0.8 },
    ]) }));
    fireEvent.change(screen.getByLabelText("Web source URL"), { target: { value: "https://example.test/faq" } });
    fireEvent.click(screen.getByRole("button", { name: "Add web page" }));
    expect(kn.addURL).toHaveBeenCalledWith("https://example.test/faq");
    fireEvent.change(screen.getByLabelText("Text"), { target: { value: "Refunds take five days." } });
    fireEvent.click(screen.getByRole("button", { name: "Add text" }));
    expect(kn.addText).toHaveBeenCalledWith("", "Refunds take five days.");
    fireEvent.change(screen.getByLabelText("File to add"), { target: { value: "f1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add file" }));
    expect(kn.addFiles).toHaveBeenCalledWith(["f1"]);
    fireEvent.change(screen.getByLabelText("Search question"), { target: { value: "Refund timing?" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByTestId("try-search").querySelector("strong")?.textContent).toBe("Five days."));
    expect(kn.search).toHaveBeenCalledWith("Refund timing?");
  });

  it("shows file ingestion errors and allows a successful retry", async () => {
    const addFile = vi.spyOn(knowledgeApi, "addFile").mockRejectedValueOnce(new Error("File could not be indexed"))
      .mockResolvedValueOnce(base.items[0]);
    const kn = knowledgeState();
    render(<Library files={filesState()} kn={kn} />);
    fireEvent.click(screen.getByRole("button", { name: "Add to knowledge" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Support" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("File could not be indexed"));
    expect(kn.select).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Add to knowledge" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Support" }));
    await waitFor(() => expect(kn.select).toHaveBeenCalledWith("kb1"));
    expect(addFile).toHaveBeenLastCalledWith("kb1", "f1");
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
