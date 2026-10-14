// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Files, MAX_UPLOAD, formatSize, kindOf } from "@/components/settings/Files";
import type { FilesState } from "@/features/files/useFiles";
import { configureClient, download, upload } from "@/lib/api/client";
import type { WorkspaceFile } from "@/types/api";

afterEach(cleanup);

const file = (over: Partial<WorkspaceFile> = {}): WorkspaceFile => ({
  id: "f1", workspace_id: "ws", name: "Plan.pdf", content_type: "application/pdf", size: 1536, created_at: "2026-10-14T10:00:00Z", ...over,
});

function state(over: Partial<FilesState> = {}): FilesState {
  return {
    loading: false, error: null, workspaces: [{ id: "ws", name: "Main", role: "member" }], workspace: { id: "ws", name: "Main", role: "member" },
    setWorkspaceId: vi.fn(), files: [], busy: null, upload: vi.fn(), remove: vi.fn(), download: vi.fn(), ...over,
  };
}

describe("Files page", () => {
  it("formats sizes and kinds", () => {
    expect([formatSize(900), formatSize(1536), formatSize(3 * 1024 * 1024)]).toEqual(["900 B", "1.5 KB", "3 MB"]);
    expect(kindOf("application/vnd.openxmlformats-officedocument.wordprocessingml.document")).toBe("Word");
    expect(kindOf("image/webp")).toBe("Image");
    expect(kindOf("application/zip")).toBe("File");
  });

  it("lists files with download and delete", () => {
    const s = state({ files: [file()] });
    render(<Files state={s} confirm={() => true} />);
    const row = screen.getByTestId("file-f1");
    expect(row.textContent).toContain("Plan.pdf");
    expect(row.textContent).toContain("PDF · 1.5 KB");
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(s.download).toHaveBeenCalledWith(file());
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(s.remove).toHaveBeenCalledWith("f1");
  });

  it("uploads picked files and refuses those over 20 MB", () => {
    const s = state();
    render(<Files state={s} />);
    const small = new File(["hello"], "notes.txt", { type: "text/plain" });
    const big = new File(["x"], "huge.pdf");
    Object.defineProperty(big, "size", { value: MAX_UPLOAD + 1 });
    fireEvent.change(screen.getByLabelText("Upload files"), { target: { files: [small, big] } });
    expect(s.upload).toHaveBeenCalledWith([small]);
    expect(screen.getByRole("alert").textContent).toContain("huge.pdf");
  });

  it("lets viewers only download", () => {
    render(<Files state={state({ files: [file()], workspace: { id: "ws", name: "Main", role: "viewer" } })} />);
    expect(screen.queryByTestId("dropzone")).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    expect(screen.getByRole("button", { name: "Download" })).toBeTruthy();
  });
});

describe("upload and download requests", () => {
  it("sends multipart with the token and reads errors", async () => {
    configureClient({ token: () => "tok", onUnauthenticated: () => {} });
    const seen: { auth: string; form: boolean }[] = [];
    const fetchImpl = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      seen.push({ auth: (init?.headers as Record<string, string>).Authorization, form: typeof (init?.body as FormData).get === "function" });
      return new Response(JSON.stringify({ error: { code: "INVALID_REQUEST", message: "The file is larger than 20 MB" } }), { status: 413 });
    });
    const form = new FormData();
    form.set("file", new Blob(["x"]), "a.txt");
    await expect(upload("/files", form, fetchImpl as typeof fetch)).rejects.toMatchObject({ status: 413, message: "The file is larger than 20 MB" });
    expect(fetchImpl.mock.calls[0][0]).toBe("/api/v1/files");
    expect(seen).toEqual([{ auth: "Bearer tok", form: true }]);

    const ok = vi.fn(async () => new Response("bytes", { status: 200 }));
    const blob = await download("/files/f1/content", ok as unknown as typeof fetch);
    expect(await blob.text()).toBe("bytes");
  });
});
