import type { NextConfig } from "next";

// The browser talks only to this Next.js server; /api/v1/* is proxied to the
// Go API (BACKEND_URL), so the backend needs no CORS and stays the only
// backend.
const backend = process.env.BACKEND_URL ?? "http://localhost:8080";

const config: NextConfig = {
  reactStrictMode: true,
  // Do not generate AGENTS.md / CLAUDE.md in the project on `next dev`.
  agentRules: false,
  experimental: {
    // Keep above handlers.PlanningTimeout (6 minutes) plus its 15-second response
    // allowance so compilation/import can return a JSON timeout before this proxy closes.
    proxyTimeout: 7 * 60 * 1000,
    // Proxied request bodies are buffered up to this size (default 10 MB);
    // file uploads go up to 20 MB (Phase D1).
    proxyClientMaxBodySize: 25 * 1024 * 1024,
  },
  // Standard browser protections on every page: never framed by another
  // site (clickjacking), no content-type guessing, no full URLs leaked to
  // other sites; microphone access requires a same-origin user gesture.
  async headers() {
    return [{
      source: "/:path*",
      headers: [
        { key: "X-Frame-Options", value: "DENY" },
        { key: "Content-Security-Policy", value: "frame-ancestors 'none'" },
        { key: "X-Content-Type-Options", value: "nosniff" },
        { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
        { key: "Permissions-Policy", value: "camera=(), microphone=(self), geolocation=()" },
      ],
    }];
  },
  async rewrites() {
    return [{ source: "/api/v1/:path*", destination: `${backend}/api/v1/:path*` }];
  },
};

export default config;
