import type { NextConfig } from "next";

/*
 * Security headers, mirroring apps/customer (issue #82 edge-security pass)
 * and apps/command (the console convention this app follows).
 *
 * CSP note: Next.js inlines its bootstrap payload, so script-src needs
 * 'unsafe-inline' today; the strict nonce-based CSP is tracked as a
 * follow-up hardening item. 'unsafe-eval' is required by React Refresh in
 * development only. Garage, like Command, never calls cross-origin APIs
 * from the browser: every upstream fetch happens in server components and
 * route handlers, so connect-src stays 'self'.
 */
const securityHeaders = [
  {
    key: "Content-Security-Policy",
    value: [
      "default-src 'self'",
      "script-src 'self' 'unsafe-inline' 'unsafe-eval'",
      "style-src 'self' 'unsafe-inline'",
      "img-src 'self' data: blob:",
      "font-src 'self' data:",
      "connect-src 'self'",
      "frame-ancestors 'none'",
      "base-uri 'self'",
      "form-action 'self'",
      "object-src 'none'",
    ].join("; "),
  },
  {
    key: "Strict-Transport-Security",
    value: "max-age=63072000; includeSubDomains; preload",
  },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  {
    key: "Permissions-Policy",
    value: "camera=(), microphone=(), geolocation=(), payment=()",
  },
];

/*
 * Base path (issue #195): on the shared production domain this console is
 * routed under /garage — the root vercel.json rewrites /garage/(.*) to
 * this service, and basePath re-roots every page, asset and Next-primitive
 * navigation (Link, router, redirect) under the same prefix. Raw browser
 * APIs (fetch(), window.location) and cookie Path attributes do NOT get
 * basePath for free, so the same value is exported as NEXT_PUBLIC_BASE_PATH
 * (src/lib/base-path.ts prefixes those call sites). Local dev serves under
 * the prefix too, so dev and prod share one URL shape.
 */
const basePath = "/garage";

const nextConfig: NextConfig = {
  basePath,
  env: { NEXT_PUBLIC_BASE_PATH: basePath },
  async headers() {
    return [
      {
        source: "/:path*",
        headers: securityHeaders,
      },
    ];
  },
};

export default nextConfig;
