package platform

import (
	"fmt"

	"go.temporal.io/sdk/client"
)

// NewTemporalClient dials the Temporal frontend using the service
// configuration. Workflows and activities are owned exclusively by their
// domain packages (ADR-0001) — the platform only provides the client.
func NewTemporalClient(cfg Config) (client.Client, error) {
	if cfg.Temporal.HostPort == "" {
		return nil, fmt.Errorf("temporal: MOTIVRA_TEMPORAL_ADDRESS is not configured")
	}
	c, err := client.Dial(client.Options{
		HostPort:  cfg.Temporal.HostPort,
		Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("temporal dial: %w", err)
	}
	return c, nil
}
