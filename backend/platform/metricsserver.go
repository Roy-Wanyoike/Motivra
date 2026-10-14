package platform

import (
	"context"
	"net/http"
	"time"
)

// StartMetricsServer serves the Prometheus exposition on its own internal
// listener (MOTIVRA_METRICS_ADDR, default ":9090"). Metrics never share the
// public API listener: runtime/process telemetry is internal information
// and the API surface must stay minimal (hardening issue #81).
//
// The returned stop function shuts the listener down inside the caller's
// shutdown budget; call it like any other cleanup. An empty addr disables
// the server and returns a no-op stop.
func StartMetricsServer(addr string, h http.Handler) (stop func(context.Context) error, err error) {
	if addr == "" || h == nil {
		return func(context.Context) error { return nil }, nil
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()
	select {
	case err = <-errCh:
		return func(context.Context) error { return nil }, err
	default:
	}
	return srv.Shutdown, nil
}
