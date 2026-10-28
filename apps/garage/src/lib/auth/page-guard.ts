/*
 * Per-page guard — the second enforcement layer (ADR-0004's two-layer rule
 * applied to the console): the proxy already denies non-garage callers, and
 * every server page independently re-verifies the session before rendering
 * anything. A caller that somehow reaches a page without a garage session
 * sees the forbidden screen, never console data.
 */

import { redirect } from "next/navigation";
import { isGarageStaff } from "./staff";
import { readSession, type Session } from "./session";

export type PageGuardResult =
  | { outcome: "allowed"; session: Session }
  | { outcome: "forbidden" }
  | { outcome: "anonymous" };

/**
 * Classify the current request for a console page. Pages translate the
 * result: anonymous → redirect to /login (carrying no data), forbidden →
 * render the ForbiddenScreen, allowed → render with the verified session.
 */
export async function classifyPageAccess(): Promise<PageGuardResult> {
  const session = await readSession();
  if (session === null) {
    return { outcome: "anonymous" };
  }
  if (!isGarageStaff(session.claims.roles)) {
    return { outcome: "forbidden" };
  }
  return { outcome: "allowed", session };
}

/** The guard pages actually call: garage staff → session; anonymous → redirect. */
export async function requireGaragePage(): Promise<Session | null> {
  const result = await classifyPageAccess();
  if (result.outcome === "anonymous") {
    redirect("/login");
  }
  if (result.outcome === "forbidden") {
    return null;
  }
  return result.session;
}
