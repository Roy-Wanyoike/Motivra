/*
 * Console base path (issue #195). next.config.ts routes this console under
 * a path prefix on the shared production domain: basePath re-roots Next.js
 * primitives (Link, router, redirect, route handlers, middleware paths)
 * automatically, but raw browser APIs — fetch(), window.location — and
 * cookie Path attributes see the raw URL and must be prefixed by hand.
 * The value is injected by next.config.ts (env.NEXT_PUBLIC_BASE_PATH) so
 * next.config.ts stays the single source of truth; unset (or empty) means
 * the console is served at the root.
 */
export const BASE_PATH = (process.env.NEXT_PUBLIC_BASE_PATH ?? "").replace(/\/+$/, "");

/** Prefix an in-console absolute path (e.g. "/login") with the base path. */
export function withBasePath(path: string): string {
  return `${BASE_PATH}${path}`;
}
