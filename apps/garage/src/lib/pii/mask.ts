/*
 * PII masking for the partner garage console (docs/PII-HANDLING.md).
 *
 * The Garage read models (GET /v1/jobs, GET /v1/payments/intents) carry
 * NO direct PII — no customer names, no phone numbers. What they do carry
 * are customer_id / technician_id / job / payment-intent references, which
 * are indirect PII ("references only" in the PII inventory). The console
 * therefore renders identifiers in a masked, pseudonymous form by default:
 *
 *     customer_····4f2a   technician_····9d31   intent_····7c05
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

/** Masked display for a payment intent reference (the invoices panel). */
export function maskIntentId(id: string | null | undefined): string {
  return maskIdentifier(id, "intent");
}

/**
 * Masked display for THIS garage's own tenant reference — used in the
 * header so staff can confirm which garage they are operating. It is the
 * garage's own identity (from the verified token), so masking here is a
 * screen-share hygiene measure, not an isolation control.
 */
export function maskGarageId(id: string | null | undefined): string {
  return maskIdentifier(id, "garage");
}
