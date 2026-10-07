"use client";

// The browser session is the backend's bearer token (Phase 12 HS256 JWT) plus
// the public user profile. It is the only thing persisted in the browser:
// provider credentials and secrets are never stored client-side.

import { create } from "zustand";
import { configureClient } from "@/lib/api/client";
import { authApi } from "@/lib/api/auth";
import type { TokenResponse, User } from "@/types/api";

const STORAGE_KEY = "workflow-optimizer.session";

interface StoredSession {
  token: string;
  expiresAt: string;
  user: User;
}

interface SessionState {
  session: StoredSession | null;
  hydrated: boolean;
  hydrate: () => void;
  login: (email: string, password: string) => Promise<void>;
  register: (email: string, name: string, password: string) => Promise<void>;
  logout: () => void;
}

function read(): StoredSession | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const s = JSON.parse(raw) as StoredSession;
    if (!s.token || Date.parse(s.expiresAt) <= Date.now()) return null;
    return s;
  } catch {
    return null;
  }
}

function write(s: StoredSession | null) {
  try {
    if (s) window.localStorage.setItem(STORAGE_KEY, JSON.stringify(s));
    else window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage unavailable: the session lives in memory only.
  }
}

function fromToken(t: TokenResponse): StoredSession {
  return { token: t.access_token, expiresAt: t.expires_at, user: t.user };
}

export const useSession = create<SessionState>((set) => ({
  session: null,
  hydrated: false,
  hydrate: () => set({ session: read(), hydrated: true }),
  login: async (email, password) => {
    const s = fromToken(await authApi.login(email, password));
    write(s);
    set({ session: s });
  },
  register: async (email, name, password) => {
    const s = fromToken(await authApi.register({ email, name, password }));
    write(s);
    set({ session: s });
  },
  logout: () => {
    write(null);
    set({ session: null });
  },
}));

configureClient({
  token: () => {
    const s = useSession.getState().session;
    return s && Date.parse(s.expiresAt) > Date.now() ? s.token : null;
  },
  // An expired or revoked token: drop the session; guards redirect to login.
  onUnauthenticated: () => useSession.getState().logout(),
});
