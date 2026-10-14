package payments

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics carries the payments domain instruments (ADR-0005 observability;
// issue #62 observability section): the payment lifecycle counters, the
// webhook receive/duplicate counters with their latency histogram, the
// reconciliation run counter and drift gauge, and the event publish
// success/failure counter. They register on the same registry as the
// platform RED instruments (platform.SetupMetrics exposes it via
// Metrics.Registry) so /metrics scrapes them together.
type Metrics struct {
	paymentsInitiated prometheus.Counter
	paymentsSucceeded prometheus.Counter
	paymentsFailed    prometheus.Counter
	paymentsRefunded  prometheus.Counter

	webhooksReceived  *prometheus.CounterVec
	webhooksDuplicate prometheus.Counter
	webhookLatency    prometheus.Histogram

	reconciliationRuns  *prometheus.CounterVec
	reconciliationDrift prometheus.Gauge

	eventsPublished *prometheus.CounterVec
}

// Webhook outcomes for webhooks_received_total.
const (
	WebhookOutcomeApplied   = "applied"
	WebhookOutcomeDuplicate = "duplicate"
	WebhookOutcomeRejected  = "rejected"
)

// Reconciliation outcomes for reconciliation_runs_total.
const (
	ReconcileClean    = "clean"
	ReconcileRepaired = "repaired"
	ReconcileError    = "error"
)

// NewMetrics builds and registers the payments domain instruments on reg.
// Register once per process (in cmd/payments via
// platform.SetupMetrics().Registry()).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		paymentsInitiated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payments_initiated_total",
			Help: "Payment intents initiated, by service.",
		}),
		paymentsSucceeded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payments_succeeded_total",
			Help: "Payment transactions settled successfully, by service.",
		}),
		paymentsFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payments_failed_total",
			Help: "Payment transactions that failed, by service.",
		}),
		paymentsRefunded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payments_refunded_total",
			Help: "Completed full refunds, by service.",
		}),
		webhooksReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payments_webhooks_received_total",
			Help: "Webhook deliveries received, by outcome (applied, duplicate, rejected).",
		}, []string{"outcome"}),
		webhooksDuplicate: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payments_webhooks_duplicate_total",
			Help: "Webhook replays that were absorbed by the idempotency key without applying.",
		}),
		webhookLatency: prometheus.Histogram(prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "payments_webhook_processing_seconds",
			Help:    "Webhook verification + apply duration in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		})),
		reconciliationRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payments_reconciliation_runs_total",
			Help: "Reconciliation runs, by outcome (clean, repaired, error).",
		}, []string{"outcome"}),
		reconciliationDrift: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "payments_reconciliation_drift",
			Help: "Intents awaiting settlement found by the last reconciliation run.",
		}),
		eventsPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payments_events_published_total",
			Help: "Domain events published by the payments service, by event type and outcome.",
		}, []string{"event_type", "outcome"}),
	}
	if reg != nil {
		reg.MustRegister(m.paymentsInitiated, m.paymentsSucceeded, m.paymentsFailed,
			m.paymentsRefunded, m.webhooksReceived, m.webhooksDuplicate, m.webhookLatency,
			m.reconciliationRuns, m.reconciliationDrift, m.eventsPublished)
	}
	return m
}

// PaymentInitiated records one initiated payment intent.
func (m *Metrics) PaymentInitiated() {
	if m != nil {
		m.paymentsInitiated.Inc()
	}
}

// PaymentSucceeded records one successfully settled transaction.
func (m *Metrics) PaymentSucceeded() {
	if m != nil {
		m.paymentsSucceeded.Inc()
	}
}

// PaymentFailed records one failed transaction.
func (m *Metrics) PaymentFailed() {
	if m != nil {
		m.paymentsFailed.Inc()
	}
}

// PaymentRefunded records one completed refund.
func (m *Metrics) PaymentRefunded() {
	if m != nil {
		m.paymentsRefunded.Inc()
	}
}

// WebhookReceived records one webhook delivery by outcome.
func (m *Metrics) WebhookReceived(outcome string) {
	if m != nil {
		m.webhooksReceived.WithLabelValues(outcome).Inc()
	}
}

// WebhookDuplicate records one absorbed replay.
func (m *Metrics) WebhookDuplicate() {
	if m != nil {
		m.webhooksDuplicate.Inc()
	}
}

// WebhookProcessed records one webhook's processing latency in seconds.
func (m *Metrics) WebhookProcessed(seconds float64) {
	if m != nil {
		m.webhookLatency.Observe(seconds)
	}
}

// ReconciliationDrift sets the drift gauge: intents awaiting settlement at
// scan time.
func (m *Metrics) ReconciliationDrift(count int) {
	if m != nil {
		m.reconciliationDrift.Set(float64(count))
	}
}

// ReconciliationRun records one reconciliation run by outcome and the
// drift still outstanding after it.
func (m *Metrics) ReconciliationRun(outcome string, drift int) {
	if m != nil {
		m.reconciliationRuns.WithLabelValues(outcome).Inc()
		m.reconciliationDrift.Set(float64(drift))
	}
}

// EventPublished records one event publish attempt as outcome "success"
// or "failure".
func (m *Metrics) EventPublished(eventType string, err error) {
	if m != nil {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		m.eventsPublished.WithLabelValues(eventType, outcome).Inc()
	}
}
