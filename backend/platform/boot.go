package platform

// Degraded boot (issue #122, ADR-0008): a Motivra service process must
// ALWAYS bind its listener and answer /healthz, even when its configuration
// or infrastructure is missing or unreachable. Configuration and dependency
// failures surface as /readyz 503 with machine-readable reasons (and 503
// problem+json on domain routes) instead of killing the process — the
// previous os.Exit(1)-on-missing-dependency posture made every Vercel
// container-routed path die with FUNCTION_INVOCATION_FAILED, including the
// dependency-free liveness endpoint.
//
// Boot owns the readiness state: named blockers (configuration and
// dependency problems — cleared by the wiring loop or by live checkers),
// non-blocking degradations (observability setup failures — the service
// serves, with reduced capability) and the domain-route gate (503
// problem+json until wiring completes, then the real API router).

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Wire retry pacing. The first retry is immediate-ish (base), backing off
// exponentially up to max so an unreachable dependency produces one probe
// every ten seconds, not a hot loop.
const (
	defaultWireBackoffBase = 250 * time.Millisecond
	defaultWireBackoffMax  = 10 * time.Second
)

// BootStep is one named step of the service wiring sequence. A step
// returning an error records a blocker named after the step (visible on
// /readyz) and aborts the current pass; the runner retries the sequence
// with backoff. Steps MUST be idempotent — a later pass re-runs every step
// that has not yet succeeded — so guard expensive one-time work on your own
// state (the cmd mains do: pool created once, publisher connected once,
// routes built once).
type BootStep struct {
	// Name is the blocker/check name reported on /readyz (e.g. "config",
	// "database", "migrations", "nats", "jwks").
	Name string
	// Permanent marks configuration-class steps whose failure cannot clear
	// without a redeploy (the process environment is fixed for the lifetime
	// of the process). A permanent blocker is recorded once and never
	// retried; other steps keep running.
	Permanent bool
	// Run executes the step. Returning nil clears the step's blocker.
	Run func(ctx context.Context) error
}

// Boot coordinates the degraded-boot lifecycle of one service process:
// readiness state, the domain-route gate and the shutdown hook registry.
// It is safe for concurrent use — wiring runs in a background goroutine
// while the HTTP server already serves.
type Boot struct {
	cfg    Config
	logger *slog.Logger
	start  time.Time

	// Retry pacing; unexported so tests can shorten the backoff.
	wireBackoffBase time.Duration
	wireBackoffMax  time.Duration

	mu           sync.RWMutex
	blockers     map[string]string             // name -> full reason (log + authenticated-operator /readyz only)
	degradations map[string]string             // name -> reason: serving, with reduced capability
	permanent    map[string]struct{}           // blockers that cannot clear without redeploy
	hooks        []func(context.Context) error // shutdown hooks, run in reverse registration order

	ready  atomic.Bool
	domain atomic.Pointer[http.Handler]
}

// NewBoot returns the boot coordinator for a service process.
func NewBoot(cfg Config, logger *slog.Logger) *Boot {
	return &Boot{
		cfg:             cfg,
		logger:          logger,
		start:           time.Now(),
		wireBackoffBase: defaultWireBackoffBase,
		wireBackoffMax:  defaultWireBackoffMax,
		blockers:        map[string]string{},
		degradations:    map[string]string{},
		permanent:       map[string]struct{}{},
	}
}

// Block records a readiness blocker. The full reason goes to the log and to
// authenticated operators only; unauthenticated /readyz bodies carry check
// NAMES exclusively (infrastructure error strings leak hosts and DSNs,
// hardening issue #81). Repeat calls with an unchanged reason do not re-log.
func (b *Boot) Block(name, reason string) {
	b.mu.Lock()
	_, existed := b.blockers[name]
	prev, _ := b.blockers[name]
	b.blockers[name] = reason
	b.mu.Unlock()
	b.ready.Store(false)
	switch {
	case !existed:
		b.logger.Error("boot_blocker", "check", name, "reason", reason)
	case prev != reason:
		b.logger.Error("boot_blocker_changed", "check", name, "reason", reason)
	}
}

// BlockPermanent records a blocker that cannot clear without a redeploy
// (configuration class). The wiring runner skips the step in future passes.
func (b *Boot) BlockPermanent(name, reason string) {
	b.mu.Lock()
	b.permanent[name] = struct{}{}
	b.mu.Unlock()
	b.Block(name, reason)
}

// isPermanent reports whether the named step already failed permanently.
func (b *Boot) isPermanent(name string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.permanent[name]
	return ok
}

// remainingBlockers returns the number of currently latched blockers.
func (b *Boot) remainingBlockers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.blockers)
}

// Unblock clears a readiness blocker (the step recovered).
func (b *Boot) Unblock(name string) {
	b.mu.Lock()
	_, existed := b.blockers[name]
	delete(b.blockers, name)
	b.mu.Unlock()
	if existed {
		b.logger.Info("boot_blocker_cleared", "check", name)
	}
}

// Degraded records a non-blocking degradation: the service serves, with
// reduced capability (e.g. no trace export). Degradations never gate
// readiness — blocking traffic because a collector is down would convert an
// observability outage into a product outage (ADR-0008).
func (b *Boot) Degraded(name, reason string) {
	b.mu.Lock()
	_, existed := b.degradations[name]
	prev := b.degradations[name]
	b.degradations[name] = reason
	b.mu.Unlock()
	switch {
	case !existed:
		b.logger.Warn("boot_degraded", "capability", name, "reason", reason)
	case prev != reason:
		b.logger.Warn("boot_degraded_changed", "capability", name, "reason", reason)
	}
}

// Ready reports whether every blocker is cleared. It is the degraded-boot
// gate for domain routes: false means every domain path answers 503.
func (b *Boot) Ready() bool { return b.ready.Load() }

// Reasons returns a snapshot of the full blocker reasons. Operators only:
// the map travels to logs and to authenticated-admin /readyz bodies, never
// to unauthenticated responses.
func (b *Boot) Reasons() map[string]string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]string, len(b.blockers))
	for name, reason := range b.blockers {
		out[name] = reason
	}
	return out
}

// PublicReasons returns the sorted blocker NAMES — the only readiness
// detail exposed without an operator identity.
func (b *Boot) PublicReasons() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	names := make([]string, 0, len(b.blockers))
	for name := range b.blockers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Degradations returns the sorted names of reduced capabilities.
func (b *Boot) Degradations() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	names := make([]string, 0, len(b.degradations))
	for name := range b.degradations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SetDomainHandler installs the wired domain API router. It is called by
// the wiring loop once the service graph is fully constructed; until then
// the Server's gate answers 503. The store is atomic, so the swap is safe
// while requests are in flight.
func (b *Boot) SetDomainHandler(h http.Handler) { b.domain.Store(&h) }

// DomainHandler returns the wired domain router, or nil before wiring
// completed.
func (b *Boot) DomainHandler() http.Handler {
	if h := b.domain.Load(); h != nil {
		return *h
	}
	return nil
}

// writeNotReady renders the unauthenticated 503 problem+json body: check
// NAMES only, no reasons (info-leak decision, ADR-0008).
func (b *Boot) writeNotReady(w http.ResponseWriter) {
	body := map[string]any{
		"code":   "not_ready",
		"title":  "Service is not ready",
		"failed": b.PublicReasons(),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(LogJSON(body)))
}

// OnShutdown registers a cleanup hook. Hooks run in reverse registration
// order when RunShutdownHooks is invoked from the graceful-shutdown path.
// The registry exists because wiring completes after the process starts
// waiting for its shutdown signal — resources acquired by the wiring loop
// (pool, publisher, redis client) must still be released.
func (b *Boot) OnShutdown(hook func(context.Context) error) {
	if hook == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hooks = append(b.hooks, hook)
}

// RunShutdownHooks runs every registered hook in reverse registration order
// and returns the first error, after running them all. It is the single
// cleanup function handed to Graceful.
func (b *Boot) RunShutdownHooks(ctx context.Context) error {
	b.mu.RLock()
	hooks := make([]func(context.Context) error, len(b.hooks))
	copy(hooks, b.hooks)
	b.mu.RUnlock()

	var firstErr error
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := hooks[i](ctx); err != nil {
			b.logger.Error("cleanup_failed", "hook", i, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Wire starts the wiring loop in a background goroutine: steps run in
// order; the first failure records a blocker and aborts the pass; passes
// retry with exponential backoff until every step succeeds (readiness
// flips) or ctx is cancelled. Permanent steps are skipped once their
// blocker is recorded.
func (b *Boot) Wire(ctx context.Context, steps ...BootStep) {
	go b.runWire(ctx, steps)
}

func (b *Boot) runWire(ctx context.Context, steps []BootStep) {
	delay := b.wireBackoffBase
	for {
		if ctx.Err() != nil {
			return
		}
		passBlocked := false
		for _, step := range steps {
			if b.isPermanent(step.Name) {
				continue
			}
			if err := b.runStep(ctx, step); err != nil {
				if step.Permanent {
					b.BlockPermanent(step.Name, err.Error())
				} else {
					b.Block(step.Name, err.Error())
				}
				passBlocked = true
				break
			}
		}
		if !passBlocked {
			// Every step is green. Readiness flips ONLY when no blocker is
			// latched: a permanent (configuration-class) blocker keeps the
			// service degraded forever — the domain gate must never serve
			// around a config problem, because /readyz 503 would be a lie.
			if b.remainingBlockers() == 0 {
				b.markReady()
			} else {
				b.logger.Error("service_degraded_permanent",
					"blockers", strings.Join(b.PublicReasons(), ","))
			}
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > b.wireBackoffMax {
			delay = b.wireBackoffMax
		}
	}
}

// runStep executes one step with panic isolation: a panicking step becomes
// a blocker (and a stack trace in the log), never a dead goroutine that
// silently wedges readiness.
func (b *Boot) runStep(ctx context.Context, step BootStep) (err error) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("boot_step_panic", "step", step.Name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	b.logger.Debug("boot_step_start", "step", step.Name)
	err = step.Run(ctx)
	if err == nil {
		b.Unblock(step.Name)
		b.logger.Debug("boot_step_ok", "step", step.Name)
	} else {
		b.logger.Debug("boot_step_failed", "step", step.Name, "error", err)
	}
	return err
}

// markReady flips readiness and logs the transition once.
func (b *Boot) markReady() {
	b.ready.Store(true)
	b.logger.Info("service_ready", "boot_ms", time.Since(b.start).Milliseconds())
}
