/*
 * PII masking for the ops console (docs/PII-HANDLING.md).
 *
 * The Command read model (GET /v1/jobs, contracts/jobs/openapi.yaml) carries
 * NO direct PII — no customer names, no phone numbers. What it does carry
 * are customer_id / technician_id references, which are indirect PII
 * ("references only" in the PII inventory). The console therefore renders
 * identifiers in a masked, pseudonymous form by default:
 *
 *     customer_····4f2a   technician_····9d31
 *
 * There is no unmask control in this MVP. When an unmask flow lands
 * (mutations follow-up), every reveal must write an audit entry first, per
 * PII-HANDLING.md.
 */

const VISIBLE_TAIL = 4;

/**
 * Render an identifier reference in masked display form. Stable for a given
 * id (same input → same output), never reveals more than the last
 * VISIBLE_TAIL alphanumeric characters.
 */
export function maskIdentifier(id: string | null | undefined, label: string): string {
  if (typeof id !== "string" || id.trim() === "") {
    return "—";
  }
  const cleaned = id.replace(/[^a-zA-Z0-9]/g, "");
  if (cleaned.length <= VISIBLE_TAIL) {
    return `${label}_····`;
  }
  return `${label}_····${cleaned.slice(-VISIBLE_TAIL).toLowerCase()}`;
}

/** Masked display for a job's owning customer reference. */
export function maskCustomerId(id: string | null | undefined): string {
  return maskIdentifier(id, "customer");
}

/** Masked display for a job's assigned technician reference. */
export function maskTechnicianId(id: string | null | undefined): string {
  return maskIdentifier(id, "technician");
}
