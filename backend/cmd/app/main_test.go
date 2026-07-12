package main

import "testing"

func TestRuntimeServicesEnabledByDefault(t *testing.T) {
	t.Setenv("SWITCHYARD_WORKER_ENABLED", "")
	t.Setenv("SWITCHYARD_SCHEDULER_ENABLED", "")
	if !envEnabledByDefault("SWITCHYARD_WORKER_ENABLED") || !envEnabledByDefault("SWITCHYARD_SCHEDULER_ENABLED") {
		t.Fatal("runtime services should be enabled when unset")
	}
	t.Setenv("SWITCHYARD_WORKER_ENABLED", "false")
	if envEnabledByDefault("SWITCHYARD_WORKER_ENABLED") {
		t.Fatal("explicit false should disable runtime service")
	}
}
