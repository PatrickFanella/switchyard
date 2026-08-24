package main

import (
	"strings"
	"testing"
)

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

func TestRunRejectsInvalidReverseProxyTokenBeforeStartup(t *testing.T) {
	const token = "invalid-proxy-token-with-newline-xxxxxxxx\nsecret"
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", token)
	err := run()
	if err == nil || !strings.Contains(err.Error(), "invalid reverse proxy authentication configuration") {
		t.Fatalf("run() error = %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("startup error leaked proxy token")
	}
}
