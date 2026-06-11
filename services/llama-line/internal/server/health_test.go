package server

import (
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
)

func TestUpstreamEntrySetHealthCountsEveryUnhealthyProbe(t *testing.T) {
	e := &upstreamEntry{}
	now := time.Now()

	e.setHealth(proxy.HealthCheckResult{Healthy: false, CheckedAt: now, Duration: time.Millisecond})
	if got := e.healthSnapshot().ConsecutiveFailures; got != 1 {
		t.Fatalf("consecutive_failures after first unhealthy probe = %d, want 1", got)
	}

	e.setHealth(proxy.HealthCheckResult{Healthy: false, CheckedAt: now.Add(time.Second), Duration: time.Millisecond})
	if got := e.healthSnapshot().ConsecutiveFailures; got != 2 {
		t.Fatalf("consecutive_failures after second unhealthy probe = %d, want 2", got)
	}

	e.setHealth(proxy.HealthCheckResult{Healthy: true, CheckedAt: now.Add(2 * time.Second), Duration: time.Millisecond})
	if got := e.healthSnapshot().ConsecutiveFailures; got != 0 {
		t.Fatalf("consecutive_failures after healthy probe = %d, want 0", got)
	}
}
