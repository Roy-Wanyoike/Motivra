"use client";

import { useState } from "react";

/*
 * Sign-out control: revokes the identity session server-side (best effort)
 * and clears the httpOnly session cookies, then lands on /login.
 */
export function LogoutButton() {
  const [busy, setBusy] = useState(false);

  async function signOut() {
    setBusy(true);
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } catch {
      // Network failure must not trap the operator on the console — the
      // cookies are cleared by the handler regardless; if this request never
      // reaches the server, navigating to /login still ends the screen share.
    } finally {
      window.location.assign("/login");
    }
  }

  return (
    <button
      type="button"
      onClick={signOut}
      disabled={busy}
      className="min-h-11 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-100 disabled:opacity-60"
    >
      {busy ? "Signing out…" : "Sign out"}
    </button>
  );
}
