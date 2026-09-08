# Motivra Business Domain Glossary

One vocabulary across every team, agent and document. Definitions here win arguments.

## People & organizations

- **Customer** — a person or organization that requests vehicle services through Motivra.
- **Technician** — a verified professional who performs inspections, diagnoses and repairs. Distinct from **Inspector**, who performs inspections only.
- **Garage** — a partner physical workshop with verified identity, capabilities and capacity.
- **Fleet** — an organization operating multiple vehicles under management, with depots and drivers.
- **Dealer** — an organization selling vehicles, whose inventory can carry verified Motivra reports.
- **Dispatcher** — an internal operator using Motivra Command to manage live jobs and escalations.

## Vehicles & data

- **Vehicle Registry** — the canonical identity system for vehicles (including VIN resolution). One vehicle, one identity.
- **Vehicle Passport** — the customer-facing summary of a vehicle's verified identity, history, evidence and intelligence. Assembled from history; never edited directly.
- **Vehicle History** — append-only record of service events. Corrections are new records; history is never silently mutated.
- **Evidence** — media and measurements (photos, videos, OBD codes, torques, odometer readings) captured during inspection or repair, stored with provenance.
- **Provenance** — who captured what, when, with which device/app version. Makes evidence verifiable rather than decorative.
- **Vehicle Intelligence** — predictions and risk signals derived from registry + history + evidence. Advisory only.

## Service flow

- **Service Request** — a customer's expression of need ("car won't start", at a location). Becomes at most one Job.
- **Job** — the operational unit of work with the lifecycle `CREATED → … → COMPLETED` (see ARCHITECTURE.md). The system of record for the service event.
- **Inspection** — structured examination of a vehicle against a template, producing findings with severity (GREEN/AMBER/RED).
- **Diagnosis** — the technician-confirmed interpretation of findings and evidence. AI may suggest; the technician decides.
- **Estimate** — itemized quote (labour, parts, callout, tax, discounts) requiring customer approval before approval-requiring work begins. `DRAFT → SENT → APPROVED | REJECTED`.
- **Escalation** — job routed from mobile repair to a garage or towing provider.
- **Warranty** — time/condition-bound guarantee attached to completed work, with claims workflow.

## Money

- **Payment** — a customer money movement with state machine `CREATED → PENDING → AUTHORIZED → PAID → FAILED → REFUNDED`. Always integer minor units, always with idempotency key + provider reference.
- **Ledger** — the authoritative financial record. AI cannot modify it. Redis cannot replace it.
- **Platform Fee** — Motivra's revenue on a transaction, visible to technicians as gross → fee → net.
- **Technician Earnings** — what a technician receives after parts and platform fee. A first-class product surface, not a report.
- **Callout** — the travel component of a mobile job, priced separately.
- **GMV** — gross value of transactions flowing through the platform. We optimize contribution margin + retention + satisfaction, never GMV alone.
- **Contribution Margin** — revenue per job minus direct costs (dispatch, payment processing, warranty, refunds). The primary economic metric.
- **Take Rate** — platform revenue divided by GMV, reported per stream.

## Platform

- **Bounded Context** — a domain with exclusive ownership of its tables, events and workflows.
- **Service Area** — the geographic zone a technician or garage serves; input to dispatch.
- **Provider** — an external service abstraction (payment, maps, notification, towing, parts supplier). Every provider has a designed failure path.
- **Country Connector** — the integration layer that makes a new market configuration, not a rewrite.
- **Integration Window** — a pause between waves for E2E, migration, security and load validation.
