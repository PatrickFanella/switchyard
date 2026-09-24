package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/runtimeconfig"
)

// Effective-value sources reported by the runtime configuration surface.
const (
	runtimeConfigSourceEnvironment   = "environment"
	runtimeConfigSourceDefault       = "default"
	runtimeConfigSourceUnset         = "unset"
	runtimeConfigSourceRuntime       = "runtime"
	runtimeConfigSourceUnavailable   = "unavailable"
	runtimeConfigSourceNotObservable = "not-observable"

	runtimeConfigChangeLimit = 50
)

// RuntimeConfigSetting is one inventoried setting with its effective value,
// source, and drift against the defaults contract. Secret values are never
// populated; only whether they are configured is reported.
type RuntimeConfigSetting struct {
	Key                string  `json:"key"`
	Component          string  `json:"component"`
	Kind               string  `json:"kind"`
	Sensitivity        string  `json:"sensitivity"`
	Mutability         string  `json:"mutability"`
	Restart            string  `json:"restart"`
	Authorization      string  `json:"authorization"`
	Description        string  `json:"description"`
	Source             string  `json:"source"`
	Effective          *string `json:"effective,omitempty"`
	Configured         *bool   `json:"configured,omitempty"`
	Default            *string `json:"default,omitempty"`
	ComposeDefault     *string `json:"compose_default,omitempty"`
	ExampleDefault     *string `json:"example_default,omitempty"`
	DeploymentOverride string  `json:"deployment_override,omitempty"`
	Overridden         *bool   `json:"overridden,omitempty"`
	Available          *bool   `json:"available,omitempty"`
	Instructions       string  `json:"instructions"`
}

// RuntimeSettingChange is the bounded, process-local audit record for one
// accepted runtime change. It is not persisted; durable audit of file-managed
// settings lives in the deployment's environment file history.
type RuntimeSettingChange struct {
	Revision uint64    `json:"revision"`
	Key      string    `json:"key"`
	Previous string    `json:"previous"`
	Value    string    `json:"value"`
	Actor    string    `json:"actor"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

// RuntimeConfigResponse is the control-listener-only effective configuration
// view.
type RuntimeConfigResponse struct {
	ContractVersion int                    `json:"contract_version"`
	Revision        uint64                 `json:"revision"`
	GeneratedAt     time.Time              `json:"generated_at"`
	Settings        []RuntimeConfigSetting `json:"settings"`
	Changes         []RuntimeSettingChange `json:"changes"`
}

// RuntimeSettings holds the revision counter and bounded audit trail for the
// runtime-mutable subset. It is shared across per-request API copies.
type RuntimeSettings struct {
	mu       sync.Mutex
	revision uint64
	changes  []RuntimeSettingChange
	now      func() time.Time
}

func newRuntimeSettings() *RuntimeSettings {
	return &RuntimeSettings{now: time.Now}
}

func (s *RuntimeSettings) snapshot() (uint64, []RuntimeSettingChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changes := make([]RuntimeSettingChange, len(s.changes))
	copy(changes, s.changes)
	return s.revision, changes
}

func (a *API) runtimeSettingsState() *RuntimeSettings {
	if a.runtimeSettings == nil {
		a.runtimeSettings = newRuntimeSettings()
	}
	return a.runtimeSettings
}

func runtimeConfigInstructions(setting runtimeconfig.Setting) string {
	edit := "Edit " + setting.Key + " in the deployed environment file (the SWITCHYARD_ENV_FILE passed to deploy/install-systemd.sh; .env for local Compose)"
	switch setting.Restart {
	case runtimeconfig.RestartNone:
		return "Change with an admin token: PUT /api/runtime-config/" + setting.Key + " with the current revision. The value is process-local and resets on API restart."
	case runtimeconfig.RestartOpenCode:
		return edit + ", then restart the OpenCode sidecar: sudo systemctl reload switchyard, or docker compose up -d opencode locally."
	case runtimeconfig.RestartCompose:
		return edit + ", then re-render Compose: sudo systemctl reload switchyard, or docker compose up -d locally. The API process cannot observe this value."
	default:
		return edit + ", then restart the API: sudo systemctl reload switchyard, or docker compose up -d api locally."
	}
}

func stringPointer(value string) *string { return &value }
func boolPointer(value bool) *bool       { return &value }

func (a *API) runtimeConfigSetting(setting runtimeconfig.Setting) RuntimeConfigSetting {
	out := RuntimeConfigSetting{
		Key:           setting.Key,
		Component:     setting.Component,
		Kind:          setting.Kind,
		Sensitivity:   setting.Sensitivity,
		Mutability:    setting.Mutability,
		Restart:       setting.Restart,
		Authorization: setting.Authorization,
		Description:   setting.Description,
		Instructions:  runtimeConfigInstructions(setting),
	}
	if setting.IsSecret() {
		out.Sensitivity = runtimeconfig.SensitivitySecret
	}
	if setting.IsRuntimeMutable() {
		value, available := a.runtimeMutableValue(setting.Key)
		out.Available = boolPointer(available)
		if available {
			out.Source = runtimeConfigSourceRuntime
			out.Effective = stringPointer(strconv.FormatBool(value))
			out.Default = stringPointer("false")
			out.Overridden = boolPointer(value)
		} else {
			out.Source = runtimeConfigSourceUnavailable
		}
		return out
	}
	if !setting.IsObservable() {
		out.Source = runtimeConfigSourceNotObservable
		if detail, ok := runtimeconfig.DefaultDetail(setting.Key); ok && !setting.IsSecret() {
			out.Default = stringPointer(detail.Runtime)
			out.ComposeDefault = stringPointer(detail.Compose)
			out.ExampleDefault = stringPointer(detail.Example)
			out.DeploymentOverride = detail.DeploymentOverride
		}
		return out
	}
	raw, present := os.LookupEnv(setting.Key)
	configured := present && strings.TrimSpace(raw) != ""
	if setting.IsSecret() {
		out.Configured = boolPointer(configured)
		if configured {
			out.Source = runtimeConfigSourceEnvironment
		} else {
			out.Source = runtimeConfigSourceUnset
		}
		return out
	}
	detail, hasDefault := runtimeconfig.DefaultDetail(setting.Key)
	if hasDefault {
		out.Default = stringPointer(detail.Runtime)
		out.ComposeDefault = stringPointer(detail.Compose)
		out.ExampleDefault = stringPointer(detail.Example)
		out.DeploymentOverride = detail.DeploymentOverride
	}
	out.Configured = boolPointer(configured)
	switch {
	case configured:
		out.Source = runtimeConfigSourceEnvironment
		out.Effective = stringPointer(raw)
		if hasDefault {
			out.Overridden = boolPointer(raw != detail.Runtime)
		}
	case hasDefault:
		out.Source = runtimeConfigSourceDefault
		out.Effective = stringPointer(detail.Runtime)
		out.Overridden = boolPointer(false)
	default:
		out.Source = runtimeConfigSourceUnset
	}
	return out
}

func (a *API) runtimeMutableValue(key string) (bool, bool) {
	switch key {
	case "scheduler.dispatch_paused":
		if a.scheduler == nil {
			return false, false
		}
		return a.scheduler.Paused(), true
	case "workers.claim_paused":
		if a.workerPool == nil {
			return false, false
		}
		return a.workerPool.Paused(), true
	default:
		return false, false
	}
}

func (a *API) applyRuntimeMutableValue(key string, value bool) {
	switch key {
	case "scheduler.dispatch_paused":
		a.scheduler.SetPaused(value)
	case "workers.claim_paused":
		a.workerPool.SetPaused(value)
	}
}

func (a *API) runtimeConfigResponse() RuntimeConfigResponse {
	revision, changes := a.runtimeSettingsState().snapshot()
	inventory := runtimeconfig.Inventory()
	settings := make([]RuntimeConfigSetting, 0, len(inventory))
	for _, setting := range inventory {
		settings = append(settings, a.runtimeConfigSetting(setting))
	}
	if changes == nil {
		changes = []RuntimeSettingChange{}
	}
	return RuntimeConfigResponse{ContractVersion: 1, Revision: revision, GeneratedAt: a.runtimeSettingsState().now().UTC(), Settings: settings, Changes: changes}
}

func (a *API) runtimeConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.runtimeConfigResponse())
}

type runtimeConfigConflict struct {
	Error    map[string]string `json:"error"`
	Revision uint64            `json:"revision"`
}

func writeRuntimeConfigConflict(w http.ResponseWriter, code, message string, revision uint64) {
	writeJSON(w, http.StatusConflict, runtimeConfigConflict{Error: map[string]string{"code": code, "message": message}, Revision: revision})
}

func (a *API) updateRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	setting, ok := runtimeconfig.InventorySetting(key)
	if !ok {
		writeError(w, http.StatusNotFound, "setting not found")
		return
	}
	state := a.runtimeSettingsState()
	if !setting.IsRuntimeMutable() {
		revision, _ := state.snapshot()
		writeRuntimeConfigConflict(w, "file-managed", setting.Key+" is file-managed. "+runtimeConfigInstructions(setting), revision)
		return
	}
	var input struct {
		Value            json.RawMessage `json:"value"`
		ExpectedRevision *uint64         `json:"expected_revision"`
		Reason           string          `json:"reason"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeJSONDecodeError(w, err)
		return
	}
	if input.ExpectedRevision == nil {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}
	var value bool
	if setting.Kind != "bool" || len(input.Value) == 0 || json.Unmarshal(input.Value, &value) != nil {
		writeError(w, http.StatusBadRequest, setting.Key+" requires a JSON boolean value")
		return
	}
	previous, available := a.runtimeMutableValue(setting.Key)
	if !available {
		revision, _ := state.snapshot()
		writeRuntimeConfigConflict(w, "unavailable", setting.Component+" is not running in this process; "+setting.Key+" cannot be changed", revision)
		return
	}
	actor := boundedAuditValue(requestActor(r))
	reason := boundedAuditValue(input.Reason)
	state.mu.Lock()
	if state.revision != *input.ExpectedRevision {
		current := state.revision
		state.mu.Unlock()
		writeRuntimeConfigConflict(w, "stale-revision", "configuration revision changed; reload and retry", current)
		return
	}
	a.applyRuntimeMutableValue(setting.Key, value)
	state.revision++
	change := RuntimeSettingChange{Revision: state.revision, Key: setting.Key, Previous: strconv.FormatBool(previous), Value: strconv.FormatBool(value), Actor: actor, Reason: reason, At: state.now().UTC()}
	state.changes = append([]RuntimeSettingChange{change}, state.changes...)
	if len(state.changes) > runtimeConfigChangeLimit {
		state.changes = state.changes[:runtimeConfigChangeLimit]
	}
	revision := state.revision
	state.mu.Unlock()
	slog.Info("runtime setting changed", "key", setting.Key, "previous", change.Previous, "value", change.Value, "actor", actor, "reason", RedactForLog(reason), "revision", revision)
	writeJSON(w, http.StatusOK, map[string]any{"setting": a.runtimeConfigSetting(setting), "revision": revision, "change": change})
}
