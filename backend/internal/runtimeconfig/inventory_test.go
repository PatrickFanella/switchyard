package runtimeconfig

import (
	"os"
	"regexp"
	"testing"
)

func TestInventoryCoversDefaultsContract(t *testing.T) {
	for _, key := range DefaultsContractKeys() {
		setting, ok := InventorySetting(key)
		if !ok {
			t.Errorf("defaults contract key %s is missing from the inventory", key)
			continue
		}
		detail, _ := DefaultDetail(key)
		if detail.Secret && !setting.IsSecret() {
			t.Errorf("%s is secret in the defaults contract but not in the inventory", key)
		}
	}
}

func TestInventoryClassificationsUseContractVocabulary(t *testing.T) {
	sensitivities := map[string]bool{SensitivityPublic: true, SensitivityInternal: true, SensitivitySecret: true}
	mutabilities := map[string]bool{MutabilityFileManaged: true, MutabilityRuntimeMutable: true}
	restarts := map[string]bool{RestartAPI: true, RestartOpenCode: true, RestartCompose: true, RestartNone: true}
	authorizations := map[string]bool{AuthorizationHostOperator: true, AuthorizationAdmin: true}
	for _, setting := range Inventory() {
		if !sensitivities[setting.Sensitivity] || !mutabilities[setting.Mutability] || !restarts[setting.Restart] || !authorizations[setting.Authorization] {
			t.Errorf("%s uses an undefined classification: %+v", setting.Key, setting)
		}
		if setting.Description == "" || setting.Component == "" || setting.Kind == "" {
			t.Errorf("%s omits component, kind, or description", setting.Key)
		}
		if setting.IsRuntimeMutable() != (setting.Restart == RestartNone) || setting.IsRuntimeMutable() != (setting.Authorization == AuthorizationAdmin) {
			t.Errorf("%s: runtime-mutable settings must require no restart and admin authorization; file-managed settings must not", setting.Key)
		}
		if SecretKeyName(setting.Key) && setting.Sensitivity != SensitivitySecret {
			t.Errorf("%s looks like a credential but is classified %s", setting.Key, setting.Sensitivity)
		}
		if setting.IsSecret() && setting.Kind != "secret" {
			t.Errorf("%s is secret but declares kind %s", setting.Key, setting.Kind)
		}
	}
}

func TestInventoryMutableSubsetIsBounded(t *testing.T) {
	var mutable []string
	for _, setting := range Inventory() {
		if setting.IsRuntimeMutable() {
			mutable = append(mutable, setting.Key)
		}
	}
	want := []string{"scheduler.dispatch_paused", "workers.claim_paused"}
	if len(mutable) != len(want) {
		t.Fatalf("runtime-mutable subset = %v, want %v", mutable, want)
	}
	for index, key := range want {
		if mutable[index] != key {
			t.Fatalf("runtime-mutable subset = %v, want %v", mutable, want)
		}
	}
}

func TestInventoryCoversEnvironmentKeysReadByTheBackend(t *testing.T) {
	// Every inventoried environment key must be spelled the same way in the
	// example environment or read by Go code; this catches typos in the
	// contract without claiming the inventory lists every variable Compose
	// or scripts consume.
	example, err := os.ReadFile("../../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range Inventory() {
		if setting.IsRuntimeMutable() {
			continue
		}
		pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(setting.Key) + `=`)
		commented := regexp.MustCompile(`(?m)^# ` + regexp.QuoteMeta(setting.Key) + `=`)
		if !pattern.Match(example) && !commented.Match(example) && !backendReadsKey(t, setting.Key) {
			t.Errorf("%s is inventoried but neither .env.example nor backend Go code references it", setting.Key)
		}
	}
}

func backendReadsKey(t *testing.T, key string) bool {
	t.Helper()
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"`)
	var found bool
	for _, dir := range []string{"../controlplane", "../../cmd/app"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || len(entry.Name()) < 3 || entry.Name()[len(entry.Name())-3:] != ".go" {
				continue
			}
			content, err := os.ReadFile(dir + "/" + entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			if pattern.Match(content) {
				found = true
			}
		}
	}
	return found
}
