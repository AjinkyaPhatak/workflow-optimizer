import type { Me, TokenResponse } from "@/types/api";
import { request } from "./client";

export const authApi = {
  login: (email: string, password: string) =>
    request<TokenResponse>("POST", "/auth/login", { body: { email, password }, anonymous: true }),
  register: (input: { email: string; name: string; password: string; workspace_name?: string }) =>
    request<TokenResponse>("POST", "/auth/register", { body: input, anonymous: true }),
  me: () => request<Me>("GET", "/auth/me"),
};
