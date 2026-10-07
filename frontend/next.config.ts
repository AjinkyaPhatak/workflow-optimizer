import type { NextConfig } from "next";

// The browser talks only to this Next.js server; /api/v1/* is proxied to the
// Go API (BACKEND_URL), so the backend needs no CORS and stays the only
// backend.
const backend = process.env.BACKEND_URL ?? "http://localhost:8080";

const config: NextConfig = {
  reactStrictMode: true,
  // Do not generate AGENTS.md / CLAUDE.md in the project on `next dev`.
  agentRules: false,
  async rewrites() {
    return [{ source: "/api/v1/:path*", destination: `${backend}/api/v1/:path*` }];
  },
};

export default config;
