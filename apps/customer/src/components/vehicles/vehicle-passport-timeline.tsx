import { VEHICLE_ENDPOINTS } from "@/lib/api/vehicles";

/*
 * Per-vehicle passport timeline PLACEHOLDER (issue #24).
 *
 * When the vehicles API is connected, this component will render:
 *   - the read-only Vehicle Passport summary from
 *       GET /v1/vehicles/{vehicleID}/passport
 *   - the append-only history from
 *       GET /v1/vehicles/{vehicleID}/history  (newest first)
 *
 * Event types below are the exact HistoryEvent enum from
 * contracts/vehicles/openapi.yaml. History is append-only: corrections are
 * new records, never edits.
 */

const HISTORY_EVENT_TYPES = [
  "service",
  "repair",
  "inspection",
  "mileage",
  "incident",
  "modification",
  "note",
] as const;

export function VehiclePassportTimeline({ vehicleId }: { vehicleId: string }) {
  const isSample = vehicleId === "sample";

  return (
    <article aria-labelledby="passport-heading" className="rounded-lg border border-slate-200 bg-white p-6">
      <p className="text-xs font-semibold uppercase tracking-widest text-amber-700">
        Read-only · assembled from registry + history
      </p>
      <h2 id="passport-heading" className="mt-1 text-2xl font-bold tracking-tight text-slate-900">
        Vehicle Passport
      </h2>
      <p className="mt-2 text-sm text-slate-600">
        Vehicle ID: <code className="rounded bg-slate-100 px-1.5 py-0.5">{vehicleId}</code>
      </p>

      {isSample ? (
        <p className="mt-4 rounded-md border border-amber-300 bg-amber-50 p-4 text-sm leading-relaxed text-slate-900">
          Sample route. This is a placeholder page for the passport timeline component — no real
          vehicle exists behind it and no data is fetched.
        </p>
      ) : null}

      <p className="mt-4 max-w-3xl text-sm leading-relaxed">
        Placeholder — no data fetched. Once connected, this timeline will render the passport
        summary from{" "}
        <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.passport}</code>{" "}
        and the newest-first history from{" "}
        <code className="rounded bg-slate-100 px-1.5 py-0.5">{VEHICLE_ENDPOINTS.history}</code>.
        Each history event carries a type, summary, optional odometer reading and optional
        evidence reference.
      </p>

      <h3 className="mt-6 text-sm font-semibold text-slate-900">
        What a timeline entry will look like (event types per contract):
      </h3>
      <ol className="mt-3 space-y-3">
        {HISTORY_EVENT_TYPES.map((eventType) => (
          <li
            key={eventType}
            className="flex flex-wrap items-center gap-3 rounded-md border border-dashed border-slate-300 bg-slate-50 px-4 py-3"
          >
            <span className="rounded bg-slate-900 px-2 py-0.5 text-xs font-bold uppercase text-amber-400">
              {eventType}
            </span>
            <span className="text-sm text-slate-600">
              Summary, odometer reading and evidence reference will appear here.
            </span>
          </li>
        ))}
      </ol>

      <p role="status" className="mt-6 text-xs font-medium text-slate-600">
        Status: placeholder component — API integration pending.
      </p>
    </article>
  );
}
