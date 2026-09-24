package runtimeconfig

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed inventory-v1.json
var rawInventory []byte

// Setting classification values. The inventory contract and the reference
// document use the same vocabulary; tests reject anything else.
const (
	SensitivityPublic   = "public"
	SensitivityInternal = "internal"
	SensitivitySecret   = "secret"

	MutabilityFileManaged    = "file-managed"
	MutabilityRuntimeMutable = "runtime-mutable"

	RestartAPI      = "api"
	RestartOpenCode = "opencode"
	RestartCompose  = "compose"
	RestartNone     = "none"

	AuthorizationHostOperator = "host-operator"
	AuthorizationAdmin        = "admin"
)

// Setting describes one configuration entry by source of truth, sensitivity,
// mutability, restart requirement, and the authorization needed to change it.
type Setting struct {
	Key           string `json:"key"`
	Component     string `json:"component"`
	Kind          string `json:"kind"`
	Sensitivity   string `json:"sensitivity"`
	Mutability    string `json:"mutability"`
	Restart       string `json:"restart"`
	Authorization string `json:"authorization"`
	Observable    *bool  `json:"observable,omitempty"`
	Description   string `json:"description"`
}

// IsSecret reports whether the value must never leave the process, combining
// the declared classification with a key-name heuristic so a misclassified
// credential-like key is still redacted.
func (s Setting) IsSecret() bool {
	return s.Sensitivity == SensitivitySecret || SecretKeyName(s.Key)
}

// IsObservable reports whether the API process can read the effective value.
// Compose-only and sidecar-only keys are inventoried but not observable.
func (s Setting) IsObservable() bool {
	return s.Observable == nil || *s.Observable
}

// IsRuntimeMutable reports whether an authorized control-plane request may
// change the value without a restart.
func (s Setting) IsRuntimeMutable() bool {
	return s.Mutability == MutabilityRuntimeMutable
}

var (
	secretKeyPattern   = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|CLIENT_KEYS|GATEWAY_KEY|API_KEY|DATABASE_URL)`)
	locationKeyPattern = regexp.MustCompile(`(?i)_(DIR|FILE|PATH)$`)
)

// SecretKeyName reports whether a key name looks like it carries a credential.
// Keys that only locate a secret file or directory are not credentials.
func SecretKeyName(key string) bool {
	return secretKeyPattern.MatchString(key) && !locationKeyPattern.MatchString(key)
}

var inventory = loadInventory()

func loadInventory() []Setting {
	var contract struct {
		Version  int       `json:"version"`
		Settings []Setting `json:"settings"`
	}
	if err := json.Unmarshal(rawInventory, &contract); err != nil || contract.Version != 1 {
		panic(fmt.Sprintf("invalid runtime inventory contract: version=%d error=%v", contract.Version, err))
	}
	seen := map[string]bool{}
	for _, setting := range contract.Settings {
		if strings.TrimSpace(setting.Key) == "" || seen[setting.Key] {
			panic("runtime inventory contract: missing or duplicate key " + setting.Key)
		}
		seen[setting.Key] = true
	}
	return contract.Settings
}

// Inventory returns a copy of every inventoried setting in contract order.
func Inventory() []Setting {
	out := make([]Setting, len(inventory))
	copy(out, inventory)
	return out
}

// InventorySetting looks up one inventoried setting by key.
func InventorySetting(key string) (Setting, bool) {
	for _, setting := range inventory {
		if setting.Key == key {
			return setting, true
		}
	}
	return Setting{}, false
}

// DefaultsContractKeys returns the sorted keys of the defaults contract so the
// inventory can be checked for completeness.
func DefaultsContractKeys() []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// DefaultDetail returns the runtime, compose, and example defaults for a key.
func DefaultDetail(name string) (DefaultValue, bool) {
	value, ok := details[name]
	return value, ok
}

// DefaultValue mirrors one defaults-contract entry including deployment and
// example variants used for drift reporting.
type DefaultValue struct {
	Runtime            string `json:"runtime"`
	Compose            string `json:"compose"`
	Example            string `json:"example"`
	DeploymentOverride string `json:"deployment_override,omitempty"`
	Secret             bool   `json:"secret,omitempty"`
}

var details = loadDefaultDetails()

func loadDefaultDetails() map[string]DefaultValue {
	var contract struct {
		Defaults map[string]DefaultValue `json:"defaults"`
	}
	if err := json.Unmarshal(rawDefaults, &contract); err != nil {
		panic("invalid runtime defaults contract: " + err.Error())
	}
	return contract.Defaults
}
