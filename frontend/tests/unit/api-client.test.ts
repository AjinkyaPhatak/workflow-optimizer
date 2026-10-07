import { describe, expect, it, vi } from "vitest";
import { ApiError, buildUrl, configureClient, request } from "@/lib/api/client";

function fakeFetch(status: number, body: unknown) {
  return vi.fn(async () => new Response(body === undefined ? null : JSON.stringify(body), { status }));
}

describe("api client", () => {
  it("builds versioned URLs and drops empty query values", () => {
    expect(buildUrl("/workflows", { project_id: "p", page: 2, page_size: undefined })).toBe("/api/v1/workflows?project_id=p&page=2");
  });

  it("sends the bearer token and JSON body", async () => {
    configureClient({ token: () => "tok", onUnauthenticated: () => {} });
    const f = fakeFetch(201, { id: "v1" });
    const res = await request<{ id: string }>("POST", "/workflows/w/versions", { body: { definition: {} }, fetchImpl: f });
    expect(res.id).toBe("v1");
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/workflows/w/versions");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer tok");
    expect(init.body).toBe(JSON.stringify({ definition: {} }));
  });

  it("does not send the token on anonymous calls", async () => {
    configureClient({ token: () => "tok", onUnauthenticated: () => {} });
    const f = fakeFetch(200, {});
    await request("POST", "/auth/login", { anonymous: true, body: {}, fetchImpl: f });
    const [, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
  });

  it("raises ApiError with the backend's code and validation findings", async () => {
    const errors = [{ code: "MISSING_REQUIRED_INPUT", message: "required input has no connection", node_id: "llm", port: "prompt" }];
    const f = fakeFetch(422, { error: { code: "VALIDATION_ERROR", message: "Workflow definition is invalid", details: { errors } } });
    const err = (await request("POST", "/x", { fetchImpl: f }).catch((e) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(422);
    expect(err.validationErrors).toEqual(errors);
  });

  it("signals 401 so the session can be dropped", async () => {
    const onUnauthenticated = vi.fn();
    configureClient({ token: () => "expired", onUnauthenticated });
    const f = fakeFetch(401, { error: { code: "UNAUTHENTICATED", message: "Authentication token has expired", details: null } });
    await expect(request("GET", "/nodes", { fetchImpl: f })).rejects.toMatchObject({ code: "UNAUTHENTICATED" });
    expect(onUnauthenticated).toHaveBeenCalledOnce();
  });

  it("returns undefined for 204 and maps network failures", async () => {
    expect(await request("DELETE", "/workflows/x", { fetchImpl: fakeFetch(204, undefined) })).toBeUndefined();
    const down = vi.fn(async () => {
      throw new TypeError("fetch failed");
    });
    await expect(request("GET", "/nodes", { fetchImpl: down })).rejects.toMatchObject({ code: "NETWORK_ERROR", status: 0 });
  });
});
