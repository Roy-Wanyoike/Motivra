import { EmptyStateCard } from "@/components/dashboard/empty-state-card";

/*
 * Dashboard home — foundation build. Each card names the contract endpoint it
 * will consume (paths verbatim from contracts/*.yaml). No mocked data.
 */

const CARDS = [
  {
    title: "Vehicles",
    description:
      "Your registered vehicles from the Vehicle Registry — one vehicle, one identity. Registering a vehicle uses POST /v1/vehicles with a VIN (ISO 3779, verified server-side), make, model and year of manufacture.",
    method: "GET",
    endpoint: "/v1/vehicles",
    note: "Now wired — the Vehicles table fetches this endpoint at runtime (keyset pagination). Passport, history and mileage reads are next.",
  },
  {
    title: "Jobs",
    description:
      "Live and past service jobs with their lifecycle — CREATED → TRIAGING → … → COMPLETED, plus CANCELLED / FAILED / ESCALATED. Service requests start at POST /v1/requests.",
    method: "GET",
    endpoint: "/v1/jobs",
    note: "Contract: contracts/jobs/openapi.yaml",
  },
  {
    title: "Vehicle Passport",
    description:
      "The read-only, customer-facing summary of a vehicle — assembled from the registry and its append-only history. Never edited directly.",
    method: "GET",
    endpoint: "/v1/vehicles/{vehicleID}/passport",
    note: "Timeline placeholder: Vehicles → passport link.",
  },
  {
    title: "Dispatch",
    description:
      "Technician scoring is explainable by design — fit, distance, equipment, past performance. You will always be able to ask why a technician was proposed.",
    method: "POST",
    endpoint: "/v1/dispatch/score",
    note: "Contract: contracts/dispatch/openapi.yaml",
  },
  {
    title: "Settings",
    description:
      "Profile and account settings. Authentication uses the identity service — sign in via POST /v1/auth/login, sessions rotate via POST /v1/auth/refresh.",
    method: "GET",
    endpoint: "/v1/users/me",
    note: "Contract: contracts/identity/openapi.yaml",
  },
] as const;

export default function DashboardPage() {
  return (
    <>
      <h1 className="text-2xl font-bold tracking-tight text-slate-900">Dashboard</h1>
      <p className="mt-2 max-w-3xl text-sm leading-relaxed">
        This is the app shell of the Motivra Drive foundation. Every area below is wired in the
        navigation and each card names the exact API endpoint it will consume once the client is
        connected to the Go backend — no mocked data, per the no-fake-UI policy.
      </p>
      <div className="mt-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {CARDS.map((card) => (
          <EmptyStateCard
            key={card.title}
            title={card.title}
            description={card.description}
            method={card.method}
            endpoint={card.endpoint}
            note={card.note}
          />
        ))}
      </div>
    </>
  );
}
