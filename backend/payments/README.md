# Payments (issue #62, Wave 5 A17)

Integer-minor-unit KES ledger over payment intents, transactions and refunds,
with a provider abstraction (M-Pesa Daraja first, deterministic sandbox for
tests/dev), a signature-verified idempotent webhook receiver and a
reconciliation fallback for missed webhooks. **No float money anywhere** —
every amount is `int64` minor units (cents), enforced in `money.go` and the
store.

## Architecture

```
cmd/payments          boot: config, PG (own migration chain), JWT, NATS,
                      reconcile ticker, graceful shutdown
backend/payments
  money.go            Money{AmountMinor int64, Currency} — arithmetic with
                      overflow/negative guards; the only place amounts math
  statemachine.go     INTENT: PENDING→PROCESSING→SUCCEEDED/FAILED/CANCELED
                      TX:     PENDING→SUCCEEDED/FAILED   REFUND: PENDING→COMPLETED/FAILED
  provider.go         Provider interface: Initiate / Query / Refund + webhook
                      normalization; adapter names "sandbox" | "daraja"
  sandbox.go          deterministic mock adapter (tests + local dev)
  daraja.go           M-Pesa Daraja adapter: STK push, status query,
                      reversal; whole-KES amounts (cents REFUSED, never
                      rounded); STK callback → normalized webhook payload
  webhook.go          HMAC-SHA256 signature verification over
                      "<timestamp>.<body>" (X-Motivra-Payments-Signature:
                      "t=<unix>,v1=<hex>"), timestamp-window replay guard
  service.go          orchestration: initiate, settle (attested-amount
                      check), refund, webhook apply, event publish
  reconcile.go        provider-query fallback: PENDING payments older than
                      a threshold are re-queried and settled/corrected
  store.go            Postgres store: append-only ledger writes, guarded
                      updates, idempotency-key claims (exactly-once webhook
                      apply), tenant-scoped reads
  http.go             chi routes (below), RBAC middleware, webhook route is
                      unauthenticated but signature-verified
  scope.go            roles: money-movers (garage/dispatcher/admin/superadmin)
                      initiate+refund; reads tenant-scoped; operators also
                      trigger reconciliation
```

Migrations: `backend/migrations/payments` (5 pairs — intents, transactions,
refunds, idempotency keys, intent transitions; append-only triggers +
guarded-update constraints).

## Routes

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/v1/payments/intents` | money-mover | initiate a payment (selects provider) |
| GET | `/v1/payments/intents` | authenticated | list/filter intents (status, customer, job, vehicle; paginated) |
| GET | `/v1/payments/intents/{intentID}` | authenticated | intent detail |
| GET | `/v1/payments/intents/{intentID}/transactions` | authenticated | transactions for an intent |
| POST | `/v1/payments/intents/{intentID}/refund` | money-mover | request a (full) refund |
| GET | `/v1/payments/transactions/{transactionID}` | authenticated | transaction detail |
| GET | `/v1/payments/refunds/{refundID}` | authenticated | refund detail |
| POST | `/v1/payments/webhooks/{provider}` | **signature only** | provider callback receiver (HMAC-verified, idempotent) |
| POST | `/v1/payments/reconcile` | operator | run the reconciliation sweep manually (also on a ticker) |

Contract: `contracts/payments/openapi.yaml` (1:1 with these routes).

## Events

JetStream subject `motivra.payments.<event_type>` (ADR-0002):
`intent.created.v1`, `transaction.succeeded.v1`, `transaction.failed.v1`,
`refund.completed.v1`, `refund.failed.v1`.

## Metrics

`payments_initiated_total`, `payments_succeeded_total`, `payments_failed_total`,
`payments_refunded_total`, `payments_webhooks_received_total`,
`payments_webhooks_duplicate_total`, `payments_webhook_processing_seconds`,
`payments_reconciliation_runs_total`, `payments_reconciliation_drift`,
`payments_events_published_total`.

## Real vs stubbed

| Component | Status | Notes |
|---|---|---|
| Ledger, state machines, idempotency, refunds | **real** | Postgres-backed, append-only, tenant-scoped |
| Sandbox provider | **real (mock)** | deterministic; used for tests and local dev; clearly labeled `sandbox` on every record |
| Daraja adapter (STK push, query, reversal) | **real code, needs owner credentials** | live only when `MOTIVRA_PAYMENTS_PROVIDER=daraja` and the Daraja env vars below are set; otherwise the service refuses to boot rather than silently faking |
| Webhook signature verification | **real** | HMAC-SHA256, constant-time compare, timestamp window |
| Notifications (payment status to customer) | **stubbed** | out of scope here — events are published; a notifications wave consumes them |
| Payouts/settlement to garage accounts | **not built** | follow-up after the core collect flow (issue scope) |
| Pricing/fee computation | **not built** | Wave 6 commerce layer |

## Daraja (M-Pesa) configuration

Provider selection and webhook secret:

- `MOTIVRA_PAYMENTS_PROVIDER` — `sandbox` (default) | `daraja`
- `MOTIVRA_PAYMENTS_WEBHOOK_SECRET` — HMAC secret for webhook verification (required outside development/test)
- `MOTIVRA_PAYMENTS_RECONCILE_INTERVAL` — sweep cadence for the missed-webhook reconciler

Daraja adapter (all required for `MOTIVRA_PAYMENTS_PROVIDER=daraja`):

- `MOTIVRA_PAYMENTS_DARAJA_BASE_URL`
- `MOTIVRA_PAYMENTS_DARAJA_CONSUMER_KEY`
- `MOTIVRA_PAYMENTS_DARAJA_CONSUMER_SECRET`
- `MOTIVRA_PAYMENTS_DARAJA_SHORTCODE`
- `MOTIVRA_PAYMENTS_DARAJA_PASSKEY`
- `MOTIVRA_PAYMENTS_DARAJA_CALLBACK_URL`
- `MOTIVRA_PAYMENTS_DARAJA_INITIATOR`
- `MOTIVRA_PAYMENTS_DARAJA_SECURITY_CREDENTIAL`
- `MOTIVRA_PAYMENTS_DARAJA_WEBHOOK_SECRET`

Credentials are **owner-side secrets**: never commit them; the sandbox
adapter is the default until the owner provisions a Daraja short code.

## Webhook setup (OWNER ACTION)

The M-Pesa/Daraja callback URL must be configured on the Safaricom portal to
point at `<public-base-url>/v1/payments/webhooks/daraja`, and the shared HMAC
secret (`MOTIVRA_PAYMENTS_DARAJA_WEBHOOK_SECRET`) must be provisioned on both
sides. Replay safety is handled by us (idempotency-key claims → exactly-once
apply; duplicate deliveries are counted, not double-settled), but **registering
the callback URL and rotating the secret are owner actions** — no code here can
do them.

## Do NOT

- Do not add float money anywhere in this package (`int64` minor units only).
- Do not trust provider responses without the attested-amount check.
- Do not log raw phone numbers (PII minimization — identifiers only).
- Do not widen the webhook route's trust: signature verification is the
  boundary, idempotency keys are the exactly-once guarantee.
