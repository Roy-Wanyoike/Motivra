/*
 * Small formatting helpers shared by the panels. All display formatting
 * happens server-side; panels receive ready-made strings, so the client
 * bundle stays free of locale logic and hydration mismatches are
 * impossible.
 */

/** "2026-10-22T09:14:00Z" → "22 Oct, 12:14 EAT" (ops desk timezone). */
export function formatTimestamp(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return new Intl.DateTimeFormat("en-KE", {
    day: "2-digit",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone: "Africa/Nairobi",
  }).format(date);
}

/**
 * Render an amount of KES minor units (integer cents) as a display string:
 * 123456 → "KES 1,234.56". Money discipline (CONTRIBUTING.md): amounts are
 * integer minor units end-to-end; this formatter uses pure integer math —
 * no floats ever touch the value — and refuses non-integer or negative
 * input rather than guessing.
 */
export function formatKes(amountMinor: number): string {
  if (!Number.isSafeInteger(amountMinor) || amountMinor < 0) {
    return "—";
  }
  const whole = Math.floor(amountMinor / 100);
  const cents = amountMinor % 100;
  const wholeStr = new Intl.NumberFormat("en-KE").format(whole);
  return `KES ${wholeStr}.${String(cents).padStart(2, "0")}`;
}
