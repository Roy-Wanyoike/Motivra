package inspections

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics carries the inspections domain instruments (ADR-0005 observability):
// session created/submitted counters, the report-assembly latency
// histogram and the event publish success/failure counter. They register on
// the same registry as the platform RED instruments (platform.SetupMetrics
// exposes it via Metrics.Registry) so /metrics scrapes them together.
type Metrics struct {
	sessionsCreated   prometheus.Counter
	sessionsSubmitted prometheus.Counter
	reportsIssued     prometheus.Counter
	reportAssembly    prometheus.Histogram
	eventsPublished   *prometheus.CounterVec
}

// NewMetrics builds and registers the inspections domain instruments on
// reg. Register once per process (in cmd/inspections via
// platform.SetupMetrics().Registry()).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		sessionsCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "inspections_sessions_created_total",
			Help: "Inspection sessions created, by service.",
		}),
		sessionsSubmitted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "inspections_sessions_submitted_total",
			Help: "Inspection sessions submitted (sealed), by service.",
		}),
		reportsIssued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "inspections_reports_issued_total",
			Help: "Verifiable inspection reports issued, by service.",
		}),
		reportAssembly: prometheus.Histogram(prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "inspections_report_assembly_seconds",
			Help:    "Report assembly (verify preconditions, snapshot, hash, persist) duration in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		})),
		eventsPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inspections_events_published_total",
			Help: "Domain events published by the inspections service, by event type and outcome.",
		}, []string{"event_type", "outcome"}),
	}
	if reg != nil {
		reg.MustRegister(m.sessionsCreated, m.sessionsSubmitted, m.reportsIssued, m.reportAssembly, m.eventsPublished)
	}
	return m
}

// SessionCreated records one created inspection session.
func (m *Metrics) SessionCreated() {
	if m != nil {
		m.sessionsCreated.Inc()
	}
}

// SessionSubmitted records one submitted (sealed) inspection session.
func (m *Metrics) SessionSubmitted() {
	if m != nil {
		m.sessionsSubmitted.Inc()
	}
}

// ReportIssued records one issued report together with its assembly
// duration in seconds.
func (m *Metrics) ReportIssued(seconds float64) {
	if m != nil {
		m.reportsIssued.Inc()
		m.reportAssembly.Observe(seconds)
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
