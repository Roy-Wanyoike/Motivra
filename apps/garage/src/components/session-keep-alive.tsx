"use client";

import { useEffect } from "react";

/*
 * Session keep-alive: quietly rotates the short-lived access token via the
 * server-side refresh handler before it expires, so an operator mid-shift
 * is never bounced to /login by the 15-minute access-token TTL. On any
 * hard failure the browser is sent to /login (no console content can load
 * without a staff session — the proxy enforces it).
 */
export function SessionKeepAlive({ intervalSeconds }: { intervalSeconds: number }) {
  useEffect(() => {
    let cancelled = false;
    const timer = setInterval(async () => {
      try {
        const response = await fetch("/api/auth/refresh", { method: "POST" });
        if (!cancelled && !response.ok && response.status !== 401) {
          // 401 means the refresh session is gone → re-auth; anything else
          // (blip) is survivable and retried next tick.
          return;
        }
        if (!cancelled && response.status === 401) {
          window.location.assign("/login");
        }
      } catch {
        // Network blip — retry next tick.
      }
    }, Math.max(30, intervalSeconds) * 1000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [intervalSeconds]);

  return null;
}
