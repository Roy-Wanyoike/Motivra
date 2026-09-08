import Link from "next/link";

/*
 * Motivra Drive — landing page.
 * Copy is condensed from the repository README (the narrative source of truth).
 * Policy: no invented statistics anywhere on this page (no-vanity-metrics).
 */

const PRODUCTS: ReadonlyArray<{ name: string; serves: string; unlocks: string }> = [
  {
    name: "Motivra Drive",
    serves: "Vehicle owners & buyers",
    unlocks: "Service requests, roadside assistance, the Vehicle Passport, history and pre-purchase inspections.",
  },
  {
    name: "Motivra Tech",
    serves: "Technicians & inspectors",
    unlocks: "Jobs, offline-first inspections, diagnostics, estimates, earnings, AI copilot.",
  },
  {
    name: "Motivra Inspect",
    serves: "Professional inspectors",
    unlocks: "Dynamic inspection templates, evidence capture, verified reports.",
  },
  {
    name: "Motivra Garage",
    serves: "Partner garages",
    unlocks: "Appointments, capacity, inventory, invoices, warranties.",
  },
  {
    name: "Motivra Fleet",
    serves: "Fleet operators",
    unlocks: "Maintenance schedules, downtime analytics, predictive service, cost per vehicle.",
  },
  {
    name: "Motivra Dealer",
    serves: "Dealerships",
    unlocks: "Verified inventory, reconditioning workflows, buyer trust.",
  },
  {
    name: "Motivra Command",
    serves: "Internal operations",
    unlocks: "Live dispatch map, escalations, incidents, system health.",
  },
  {
    name: "Motivra Intelligence",
    serves: "The platform itself",
    unlocks: "Diagnostic assistance, predictive maintenance, pricing and demand intelligence — advisory only.",
  },
  {
    name: "Motivra Developer",
    serves: "External partners",
    unlocks: "APIs, webhooks, sandbox, SDKs — Motivra as infrastructure.",
  },
];

const MVP_LOOP: ReadonlyArray<{ step: string; detail: string }> = [
  {
    step: "Register",
    detail:
      "A customer registers and adds their vehicle by VIN — the Vehicle Passport appears immediately.",
  },
  {
    step: "Report",
    detail:
      "One tap: “My car won’t start.” AI triage is advisory — possible causes with confidence and source, never a verdict.",
  },
  {
    step: "Dispatch",
    detail:
      "Dispatch scores technicians on fit, distance, equipment and past performance — explainably, never opaquely.",
  },
  {
    step: "Inspect",
    detail:
      "The technician arrives, inspects with photo evidence, and diagnoses.",
  },
  {
    step: "Approve & pay",
    detail:
      "The customer approves the estimate before a single bolt turns, then pays via M-Pesa.",
  },
  {
    step: "Record",
    detail:
      "Warranty, service report, Vehicle Passport update and a maintenance recommendation close the loop.",
  },
];

const AUDIENCES: ReadonlyArray<{ title: string; body: string }> = [
  {
    title: "For vehicle owners",
    body:
      "The garage comes to you. Request help in one tap, approve every estimate before work starts, and hold a Vehicle Passport that grows more valuable with every verified service — proof that follows the vehicle, not the paper trail.",
  },
  {
    title: "For garages & technicians",
    body:
      "Motivra organizes supply instead of fighting it: demand aggregation, verified trust signals, explainable quality scoring that rewards good work, and earnings that are always visible as gross → fee → net.",
  },
  {
    title: "For investors",
    body:
      "Motivra is an infrastructure play: vehicle identity, append-only history and a data flywheel where every job makes the next job better. Whoever builds that layer becomes the system of record for the vehicles it serves.",
  },
];

export default function LandingPage() {
  return (
    <>
      <header className="bg-slate-950 text-slate-200">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-4 sm:px-6">
          <p className="text-lg font-bold tracking-tight text-white">
            Motivra <span className="text-amber-400">Drive</span>
          </p>
          <nav aria-label="Site">
            <ul className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm">
              <li>
                <a className="underline-offset-4 hover:underline" href="#problem">
                  Why it exists
                </a>
              </li>
              <li>
                <a className="underline-offset-4 hover:underline" href="#products">
                  Products
                </a>
              </li>
              <li>
                <a className="underline-offset-4 hover:underline" href="#how-it-works">
                  How it works
                </a>
              </li>
              <li>
                <a className="underline-offset-4 hover:underline" href="#honest-status">
                  Honest status
                </a>
              </li>
              <li>
                <Link
                  className="rounded-md border border-slate-700 px-3 py-1.5 font-semibold text-white hover:border-amber-400 hover:text-amber-300"
                  href="/login"
                >
                  Sign in
                </Link>
              </li>
            </ul>
          </nav>
        </div>
      </header>

      <main id="main-content">
        {/* Hero */}
        <section className="bg-slate-950 text-slate-200" aria-labelledby="hero-heading">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6 sm:py-24">
            <p className="text-sm font-semibold uppercase tracking-widest text-amber-400">
              Vehicle intelligence &amp; service infrastructure for Africa
            </p>
            <h1
              id="hero-heading"
              className="mt-4 max-w-3xl text-4xl font-extrabold tracking-tight text-white sm:text-5xl"
            >
              The garage comes to you — and the vehicle never forgets.
            </h1>
            <p className="mt-6 max-w-2xl text-lg leading-relaxed">
              One tap: “My car won’t start.” A verified technician arrives with the right skills,
              equipment and parts. You approve the estimate before a single bolt turns — and every
              completed repair is written permanently to the vehicle’s{" "}
              <strong className="font-semibold text-white">Vehicle Passport</strong>, so the next
              owner, insurer or mechanic inherits a vehicle with a memory instead of a mystery.
            </p>
            <div className="mt-8 flex flex-wrap gap-3">
              <Link
                href="/dashboard"
                className="rounded-md bg-amber-600 px-5 py-2.5 font-semibold text-white hover:bg-amber-500"
              >
                Open the dashboard
              </Link>
              <a
                href="#how-it-works"
                className="rounded-md border border-slate-700 px-5 py-2.5 font-semibold text-white hover:border-amber-400 hover:text-amber-300"
              >
                See the first loop
              </a>
            </div>
          </div>
        </section>

        {/* Problem — qualitative and honest, no invented statistics */}
        <section id="problem" className="bg-white" aria-labelledby="problem-heading">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6">
            <h2
              id="problem-heading"
              className="text-3xl font-bold tracking-tight text-slate-900"
            >
              Vehicle ownership runs on a broken service layer
            </h2>
            <p className="mt-4 max-w-3xl text-lg leading-relaxed">
              Across African markets there is no trusted way to answer three questions:{" "}
              <em>who can fix this car, what exactly is wrong with it, and what has been done to
              it before?</em>
            </p>
            <ul className="mt-6 max-w-3xl space-y-4 text-base leading-relaxed">
              <li className="flex gap-3">
                <span aria-hidden="true" className="mt-1 h-2 w-2 shrink-0 rounded-full bg-amber-600" />
                <span>
                  Mechanics are skilled but invisible — no verified history, no ratings that
                  matter, no economics that let them invest in tools.
                </span>
              </li>
              <li className="flex gap-3">
                <span aria-hidden="true" className="mt-1 h-2 w-2 shrink-0 rounded-full bg-amber-600" />
                <span>
                  Owners react to breakdowns instead of preventing them. Fleets bleed margin into
                  unplanned downtime.
                </span>
              </li>
              <li className="flex gap-3">
                <span aria-hidden="true" className="mt-1 h-2 w-2 shrink-0 rounded-full bg-amber-600" />
                <span>
                  Used-car buyers pay for vehicles whose odometers lie, because there is no
                  tamper-evident service record anywhere in the market.
                </span>
              </li>
            </ul>
            <p className="mt-6 max-w-3xl text-lg leading-relaxed text-slate-900">
              This is not a UX problem. It is a{" "}
              <strong className="font-semibold">missing infrastructure layer</strong>: vehicle
              identity, verified service history, and a coordinated service network. Motivra is
              being built to be that layer — the mobile mechanic is the entry point, the wedge
              that creates transactions and trust.
            </p>
          </div>
        </section>

        {/* Product surface summary — condensed from the README’s 9-product table */}
        <section id="products" className="bg-slate-50" aria-labelledby="products-heading">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6">
            <h2
              id="products-heading"
              className="text-3xl font-bold tracking-tight text-slate-900"
            >
              One platform. Nine product surfaces.
            </h2>
            <p className="mt-4 max-w-3xl text-base leading-relaxed">
              Every surface runs on the same event-driven infrastructure, across one lifecycle:
              discover → verify → inspect → buy → own → maintain → repair → operate → sell →
              verify again. Motivra Drive — this app — is the customer surface.
            </p>
            <div className="mt-8 overflow-x-auto rounded-lg border border-slate-200 bg-white">
              <table className="w-full min-w-[40rem] text-left text-sm">
                <caption className="sr-only">
                  Motivra product surfaces, who each serves, and what each unlocks
                </caption>
                <thead className="bg-slate-100 text-slate-900">
                  <tr>
                    <th scope="col" className="px-4 py-3 font-semibold">Product</th>
                    <th scope="col" className="px-4 py-3 font-semibold">Serves</th>
                    <th scope="col" className="px-4 py-3 font-semibold">What it unlocks</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-200">
                  {PRODUCTS.map((product) => (
                    <tr key={product.name} className={product.name === "Motivra Drive" ? "bg-amber-50" : undefined}>
                      <th scope="row" className="px-4 py-3 font-semibold text-slate-900">
                        {product.name}
                        {product.name === "Motivra Drive" ? (
                          <span className="ml-2 rounded-full bg-amber-600 px-2 py-0.5 text-xs font-semibold text-white">
                            this app
                          </span>
                        ) : null}
                      </th>
                      <td className="px-4 py-3 text-slate-700">{product.serves}</td>
                      <td className="px-4 py-3 text-slate-700">{product.unlocks}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </section>

        {/* How it works — the MVP loop */}
        <section id="how-it-works" className="bg-white" aria-labelledby="how-heading">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6">
            <h2 id="how-heading" className="text-3xl font-bold tracking-tight text-slate-900">
              The first loop we ship
            </h2>
            <p className="mt-4 max-w-3xl text-base leading-relaxed">
              The MVP is not a feature list — it is one loop, executed exceptionally well:
            </p>
            <ol className="mt-8 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {MVP_LOOP.map((item, index) => (
                <li
                  key={item.step}
                  className="rounded-lg border border-slate-200 bg-slate-50 p-5"
                >
                  <p className="flex items-baseline gap-3">
                    <span
                      aria-hidden="true"
                      className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-slate-900 text-sm font-bold text-amber-400"
                    >
                      {index + 1}
                    </span>
                    <span className="text-base font-semibold text-slate-900">{item.step}</span>
                  </p>
                  <p className="mt-2 text-sm leading-relaxed">{item.detail}</p>
                </li>
              ))}
            </ol>
            <p className="mt-8 max-w-3xl rounded-lg border-l-4 border-amber-600 bg-slate-50 p-4 text-sm leading-relaxed text-slate-900">
              <strong className="font-semibold">Definition of done for the first milestone:</strong>{" "}
              a real customer requests help, a real technician completes the job, the customer
              approves and pays, and Motivra records the entire service lifecycle reliably.
              Everything else is built on top of that loop.
            </p>
          </div>
        </section>

        {/* Audience split */}
        <section id="audiences" className="bg-slate-50" aria-labelledby="audiences-heading">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6">
            <h2
              id="audiences-heading"
              className="text-3xl font-bold tracking-tight text-slate-900"
            >
              Built for three audiences
            </h2>
            <div className="mt-8 grid gap-6 md:grid-cols-3">
              {AUDIENCES.map((audience) => (
                <article
                  key={audience.title}
                  className="rounded-lg border border-slate-200 bg-white p-6"
                >
                  <h3 className="text-lg font-semibold text-slate-900">{audience.title}</h3>
                  <p className="mt-3 text-sm leading-relaxed">{audience.body}</p>
                </article>
              ))}
            </div>
          </div>
        </section>

        {/* Honest status — mirrors the README policy */}
        <section id="honest-status" className="bg-white" aria-labelledby="status-heading">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6">
            <h2 id="status-heading" className="text-3xl font-bold tracking-tight text-slate-900">
              Honest status
            </h2>
            <div className="mt-6 max-w-3xl space-y-4 rounded-lg border border-amber-200 bg-amber-50 p-6 text-base leading-relaxed text-slate-900">
              <p>
                This repository was bootstrapped in <strong>September 2026</strong>. It is at the
                very beginning: architecture and engineering governance land first, then
                implementation waves. We hold ourselves to a{" "}
                <strong>no-vanity-metrics policy</strong> — when numbers appear, they will be real
                customers, real jobs, real revenue, real retention.
              </p>
              <p>
                This web foundation is part of that discipline. The landing page carries the real
                narrative. The sign-in form and dashboard shell are working UI scaffolds with
                explicit “API integration pending” states — no mocked data, no fake dashboards.
                They are shaped against the OpenAPI contracts the Go backend already serves in{" "}
                <code className="rounded bg-white px-1.5 py-0.5 text-sm">contracts/</code>.
              </p>
              <p>
                Until the backend is wired in, judge this surface on its semantics, accessibility
                and honesty — the same way we expect to be judged after launch.
              </p>
            </div>
          </div>
        </section>
      </main>

      <footer className="bg-slate-950 text-slate-400">
        <div className="mx-auto max-w-6xl px-4 py-10 sm:px-6">
          <p className="text-base font-semibold text-white">
            Motivra <span className="text-amber-400">Drive</span>
          </p>
          <p className="mt-2 max-w-2xl text-sm leading-relaxed">
            The garage comes to you — and the vehicle never forgets. Vehicle intelligence &amp;
            service infrastructure for Africa, starting in Nairobi, designed for expansion.
          </p>
          <nav aria-label="Footer" className="mt-6">
            <ul className="flex flex-wrap gap-x-6 gap-y-2 text-sm">
              <li>
                <a
                  className="underline-offset-4 hover:text-white hover:underline"
                  href="https://github.com/Roy-Wanyoike/Motivra"
                >
                  GitHub repository
                </a>
              </li>
              <li>
                <a
                  className="underline-offset-4 hover:text-white hover:underline"
                  href="https://github.com/Roy-Wanyoike/Motivra/tree/main/contracts"
                >
                  API contracts (OpenAPI)
                </a>
              </li>
              <li>
                <a
                  className="underline-offset-4 hover:text-white hover:underline"
                  href="https://github.com/Roy-Wanyoike/Motivra/issues"
                >
                  Issue tracker
                </a>
              </li>
            </ul>
          </nav>
          <p className="mt-6 text-xs">
            Status: pre-launch foundation. No fake metrics — see the honest-status section above.
          </p>
        </div>
      </footer>
    </>
  );
}
