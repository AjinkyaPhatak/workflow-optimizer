import type { Usage, User, WorkspaceRef } from "@/types/api";
import { request } from "./client";

/** Account settings and usage (Phase M). */
export const accountApi = {
  updateProfile: (name: string) => request<User>("PATCH", "/auth/me", { body: { name } }),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>("POST", "/auth/password", { body: { current_password: currentPassword, new_password: newPassword } }),
  renameWorkspace: (workspaceId: string, name: string) =>
    request<WorkspaceRef>("PATCH", `/workspaces/${encodeURIComponent(workspaceId)}`, { body: { name } }),
  usage: (workspaceId: string, days: number) =>
    request<Usage>("GET", `/workspaces/${encodeURIComponent(workspaceId)}/usage`, { query: { days } }),
};
