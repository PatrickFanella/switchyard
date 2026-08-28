package runtimeconfig

import (
	"testing"
	"time"
)

func TestCriticalRuntimeDefaults(t *testing.T) {
	for name, want := range map[string]string{
		"SWITCHYARD_LISTEN_ADDR":          ":8080",
		"SWITCHYARD_WORKER_ENABLED":       "true",
		"SWITCHYARD_SCHEDULER_ENABLED":    "true",
		"SWITCHYARD_RETENTION":            "720h",
		"MODEL_BROKER_QUEUE_MAX_DEPTH":    "100",
		"MODEL_BROKER_RETRY_ATTEMPTS":     "1",
		"OPENCODE_SESSION_CREATE_TIMEOUT": "5s",
		"OPENCODE_ABORT_TIMEOUT":          "1s",
		"OPEN_PILOT_COMMAND_POLICY_MODE":  "deny",
		"OPEN_PILOT_COMMAND_TIMEOUT":      "120s",
		"OPEN_PILOT_WORKTREE_TIMEOUT":     "120s",
		"OPEN_PILOT_CI_AUTO_REPAIR_MODE":  "disabled",
	} {
		if got := String(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := Duration("MODEL_BROKER_RETRY_BACKOFF"); got != 250*time.Millisecond {
		t.Errorf("MODEL_BROKER_RETRY_BACKOFF = %s", got)
	}
	if got := Int("MODEL_BROKER_QUEUE_WORKERS"); got != 1 {
		t.Errorf("MODEL_BROKER_QUEUE_WORKERS = %d", got)
	}
}
