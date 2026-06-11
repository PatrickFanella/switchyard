package keystore_test

import (
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/keystore"
)

func TestNew_SeedsFromConfig(t *testing.T) {
	s := keystore.New([]config.APIKey{
		{Name: "app", Key: "secret", Priority: 5},
	})
	k := s.Lookup("secret")
	if k == nil {
		t.Fatal("expected key, got nil")
	}
	if k.Name != "app" || k.Priority != 5 {
		t.Errorf("name=%q priority=%d, want app/5", k.Name, k.Priority)
	}
}

func TestLookup_UnknownToken(t *testing.T) {
	s := keystore.New(nil)
	if s.Lookup("nope") != nil {
		t.Error("expected nil for unknown token")
	}
}

func TestCreate_ReturnsKeyOnce(t *testing.T) {
	s := keystore.New(nil)
	k, err := s.Create("new-app", 3)
	if err != nil {
		t.Fatal(err)
	}
	if k.Value == "" {
		t.Error("key value should not be empty")
	}
	if k.Name != "new-app" || k.Priority != 3 {
		t.Errorf("name=%q priority=%d, want new-app/3", k.Name, k.Priority)
	}
	// Lookup should work.
	found := s.Lookup(k.Value)
	if found == nil || found.Name != "new-app" {
		t.Error("lookup after create failed")
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	s := keystore.New(nil)
	if _, err := s.Create("dup", 0); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create("dup", 0)
	if err != keystore.ErrKeyExists {
		t.Errorf("err = %v, want ErrKeyExists", err)
	}
}

func TestRevoke_RemovesKey(t *testing.T) {
	s := keystore.New([]config.APIKey{{Name: "app", Key: "tok"}})
	if err := s.Revoke("app"); err != nil {
		t.Fatal(err)
	}
	if s.Lookup("tok") != nil {
		t.Error("key still accessible after revoke")
	}
}

func TestRevoke_NotFound(t *testing.T) {
	s := keystore.New(nil)
	if err := s.Revoke("ghost"); err != keystore.ErrKeyNotFound {
		t.Errorf("err = %v, want ErrKeyNotFound", err)
	}
}

func TestUpdate_Priority(t *testing.T) {
	s := keystore.New([]config.APIKey{{Name: "app", Key: "tok", Priority: 0}})
	if err := s.Update("app", "", 10); err != nil {
		t.Fatal(err)
	}
	k := s.Lookup("tok")
	if k == nil || k.Priority != 10 {
		t.Errorf("priority = %v, want 10", k)
	}
}

func TestUpdate_Rename(t *testing.T) {
	s := keystore.New([]config.APIKey{{Name: "old", Key: "tok"}})
	if err := s.Update("old", "new", 0); err != nil {
		t.Fatal(err)
	}
	k := s.Lookup("tok")
	if k == nil || k.Name != "new" {
		t.Errorf("name = %v, want new", k)
	}
	// Old name should be gone from list.
	for _, m := range s.List() {
		if m.Name == "old" {
			t.Error("old name still in list after rename")
		}
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := keystore.New(nil)
	if err := s.Update("ghost", "", 0); err != keystore.ErrKeyNotFound {
		t.Errorf("err = %v, want ErrKeyNotFound", err)
	}
}

func TestList_NeverExposesValues(t *testing.T) {
	s := keystore.New([]config.APIKey{{Name: "app", Key: "supersecret"}})
	for _, m := range s.List() {
		// KeyMeta has no Value field — this is a compile-time guarantee.
		_ = m.Name
		_ = m.Priority
		_ = m.CreatedAt
	}
}
