/*
 * Command audit trail (issue #123 acceptance: audit entries for every view
 * of PII-bearing data; docs/PII-HANDLING.md).
 *
 * MVP scope: the console renders masked identifier references only — there
 * is no direct PII (names, phones) on screen and no unmask control, so
 * there is nothing to redact from these events. What IS audited, one
 * structured log line each (stdout → Loki, ADR-0005; no raw PII values in
 * logs, per PII-HANDLING.md "Where PII must never appear"):
 *
 *   - auth.login          staff login succeeded
 *   - auth.login_denied   authenticated caller without a staff role
 *   - auth.logout         session revoked
 *   - console.view        every console render (actor + route + filters —
 *                         the operator was looking at customer-linked
 *                         operational data)
 *
 * When an unmask/reveal flow ships, its audit rows must move to the
 * identity service's append-only audit_log (no write API exists today —
 * the gap is tracked in the incidents/mutations follow-up).
 */

export interface AuditEvent {
  action: string;
  /** Token subject of the acting principal (never the raw token). */
  actor: string;
  route?: string;
  detail?: Record<string, string | number | boolean | null>;
}

export function audit(event: AuditEvent): void {
  const entry = {
    ts: new Date().toISOString(),
    service: "command",
    kind: "audit",
    action: event.action,
    actor: event.actor,
    ...(event.route === undefined ? {} : { route: event.route }),
    ...(event.detail === undefined ? {} : { detail: event.detail }),
  };
  // Single structured line; the platform log pipeline ships it to Loki.
  console.log(JSON.stringify(entry));
}
