interface EmptyStateCardProps {
  title: string;
  description: string;
  /** HTTP method the future integration will use, e.g. "GET". */
  method: string;
  /** Contract path the future integration will consume, e.g. "/v1/vehicles". */
  endpoint: string;
  note?: string;
}

/*
 * Empty-state card for the dashboard foundation: names the exact API
 * endpoint the card will consume once the client is wired to the backend.
 */
export function EmptyStateCard({
  title,
  description,
  method,
  endpoint,
  note,
}: EmptyStateCardProps) {
  return (
    <article className="flex h-full flex-col rounded-lg border border-slate-200 bg-white p-6">
      <h3 className="text-base font-semibold text-slate-900">{title}</h3>
      <p className="mt-2 flex-1 text-sm leading-relaxed">{description}</p>
      <p className="mt-4">
        <span className="mr-2 rounded bg-slate-900 px-2 py-0.5 text-xs font-bold text-amber-400">
          {method}
        </span>
        <code className="rounded bg-slate-100 px-2 py-1 text-xs text-slate-800">
          {endpoint}
        </code>
      </p>
      {note ? <p className="mt-3 text-xs leading-relaxed text-slate-600">{note}</p> : null}
    </article>
  );
}
