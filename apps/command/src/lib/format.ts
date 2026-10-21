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
