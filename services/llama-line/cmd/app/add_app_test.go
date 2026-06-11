package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAppendAppKeyToConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := "listen_addr: :11434\nkeys:\n  - name: existing\n    key: old\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	settings := appKeySettings{
		Name:            "new-app",
		Key:             "11111111-2222-4333-8444-555555555555",
		Priority:        7,
		Deduplicate:     true,
		CacheTTLSecs:    60,
		CacheMaxEntries: 100,
	}
	if err := appendAppKeyToConfig(path, settings); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Keys []struct {
			Name            string `yaml:"name"`
			Key             string `yaml:"key"`
			Priority        int    `yaml:"priority"`
			Deduplicate     bool   `yaml:"deduplicate"`
			CacheTTLSecs    int    `yaml:"cache_ttl_secs"`
			CacheMaxEntries int    `yaml:"cache_max_entries"`
		} `yaml:"keys"`
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(got.Keys))
	}
	added := got.Keys[1]
	if added.Name != settings.Name || added.Key != settings.Key || added.Priority != settings.Priority || !added.Deduplicate || added.CacheTTLSecs != settings.CacheTTLSecs || added.CacheMaxEntries != settings.CacheMaxEntries {
		t.Fatalf("added key = %+v", added)
	}
}

func TestAppendAppKeyRejectsDuplicateName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("keys:\n  - name: existing\n    key: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := appendAppKeyToConfig(path, appKeySettings{Name: "existing", Key: "new"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
}

func TestRunAddAppNoReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("keys: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader("test-app\n5\ny\n30\n40\n")
	out := &strings.Builder{}
	if err := runAddApp([]string{"--config", path, "--no-reload"}, in, out, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reload skipped") {
		t.Fatalf("output = %q, want reload skipped", out.String())
	}
	match := regexp.MustCompile(`api key: [0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`).MatchString(out.String())
	if !match {
		t.Fatalf("output missing uuid api key: %q", out.String())
	}
}
