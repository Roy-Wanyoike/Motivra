import type { ReactNode } from "react";

/*
 * Shared panel chrome + the degraded states every panel can show
 * (issue #124, following the Command panel pattern: degraded backends must
 * not crash panels — each panel owns its own error/empty/pending state).
 */

export interface PanelError {
  code: string;
  title: string;
  unreachable: boolean;
  /** HTTP status when the failure came from a response (0 = unreachable). */
  status?: number;
}

export function PanelSection({
  id,
  title,
  description,
  status,
  children,
}: {
  id: string;
  title: string;
  description: string;
  status?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-heading`}
      className="rounded-lg border border-slate-200 bg-white p-4 shadow-sm sm:p-6"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 id={`${id}-heading`} className="text-lg font-bold tracking-tight text-slate-900">
            {title}
          </h2>
          <p className="mt-1 max-w-2xl text-sm leading-relaxed text-slate-600">{description}</p>
        </div>
        {status === undefined ? null : <div className="text-sm text-slate-500">{status}</div>}
      </div>
      <div className="mt-4">{children}</div>
    </section>
  );
}

export function PanelErrorState({ error, hint }: { error: PanelError; hint: string }) {
  return (
    <div
      role="status"
      className="rounded-md border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900"
    >
      <p className="font-semibold">
        {error.unreachable ? "Service unreachable" : "Service error"} — this panel is degraded
      </p>
      <p className="mt-1 leading-relaxed">
        {error.title} <span className="text-amber-700">({error.code})</span>
      </p>
      <p className="mt-1 leading-relaxed text-amber-800">{hint}</p>
    </div>
  );
}

export function PanelEmptyState({ message }: { message: string }) {
  return (
    <p className="rounded-md border border-slate-200 bg-slate-50 p-4 text-sm text-slate-600">
      {message}
    </p>
  );
}

/**
 * The honest "backend capability pending" state (the Command MVP pattern
 * for panels whose backend does not exist yet — issue #124: no invented
 * APIs, no fake data). Says exactly what is missing and where it is
 * tracked.
 */
export function PendingCapabilityState({
  capability,
  trackingIssue,
  trackingLabel,
  detail,
}: {
  capability: string;
  trackingIssue: string;
  trackingLabel: string;
  detail: string;
}) {
  return (
    <div className="rounded-md border border-slate-200 bg-slate-50 p-4 text-sm leading-relaxed text-slate-600">
      <p className="font-semibold text-slate-800">Backend capability pending — {capability}</p>
      <p className="mt-2">{detail}</p>
      <p className="mt-2">
        Tracked in{" "}
        <a
          className="font-semibold text-amber-800 underline underline-offset-4"
          href={trackingIssue}
          rel="noreferrer"
        >
          {trackingLabel}
        </a>
        . This panel will render live data as soon as that capability ships — no console change
        is planned beyond wiring it up.
      </p>
    </div>
  );
}
