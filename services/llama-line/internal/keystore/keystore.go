// Package keystore provides a concurrency-safe in-memory API key store.
// Keys are seeded from config at startup. Runtime changes are held in memory
// only and reset on restart unless --persist-keys is used.
package keystore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

// ErrKeyNotFound is returned when a key name does not exist.
var ErrKeyNotFound = errors.New("key not found")

// ErrKeyExists is returned when creating a key with a name that already exists.
var ErrKeyExists = errors.New("key name already exists")

// Key is a single API key entry.
type Key struct {
	Name            string    `json:"name"`
	Value           string    `json:"-"` // never serialised in list responses
	Priority        int       `json:"priority"`
	Deduplicate     bool      `json:"deduplicate"`
	CacheTTLSecs    int       `json:"cache_ttl_secs"`
	CacheMaxEntries int       `json:"cache_max_entries"`
	CreatedAt       time.Time `json:"created_at"`
}

// KeyMeta is the public view of a key (no value).
type KeyMeta struct {
	Name            string    `json:"name"`
	Priority        int       `json:"priority"`
	Deduplicate     bool      `json:"deduplicate"`
	CacheTTLSecs    int       `json:"cache_ttl_secs"`
	CacheMaxEntries int       `json:"cache_max_entries"`
	CreatedAt       time.Time `json:"created_at"`
}

// Store is a concurrency-safe in-memory key store.
type Store struct {
	mu      sync.RWMutex
	keys    map[string]*Key // keyed by Name
	byValue map[string]*Key // keyed by Value (for auth lookup)
}

// New creates a Store seeded from config keys.
func New(cfgKeys []config.APIKey) *Store {
	s := &Store{
		keys:    make(map[string]*Key, len(cfgKeys)),
		byValue: make(map[string]*Key, len(cfgKeys)),
	}
	for _, k := range cfgKeys {
		entry := &Key{
			Name:            k.Name,
			Value:           k.Key,
			Priority:        k.Priority,
			Deduplicate:     k.Deduplicate,
			CacheTTLSecs:    k.CacheTTLSecs,
			CacheMaxEntries: k.CacheMaxEntries,
			CreatedAt:       time.Now(),
		}
		s.keys[k.Name] = entry
		s.byValue[k.Key] = entry
	}
	return s
}

// Lookup returns the Key for a given token value, or nil if not found.
func (s *Store) Lookup(value string) *Key {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byValue[value]
}

// Get returns a copy of the Key for a given name, or nil if not found.
func (s *Store) Get(name string) *Key {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[name]
	if !ok {
		return nil
	}
	copy := *k
	return &copy
}

// List returns metadata for all keys, sorted by name.
func (s *Store) List() []KeyMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]KeyMeta, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, KeyMeta{
			Name:            k.Name,
			Priority:        k.Priority,
			Deduplicate:     k.Deduplicate,
			CacheTTLSecs:    k.CacheTTLSecs,
			CacheMaxEntries: k.CacheMaxEntries,
			CreatedAt:       k.CreatedAt,
		})
	}
	return out
}

// Create generates a new key with the given name and priority.
// Returns the full Key (including Value) exactly once.
// Returns ErrKeyExists if the name is already taken.
func (s *Store) Create(name string, priority int) (*Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.keys[name]; exists {
		return nil, ErrKeyExists
	}
	value, err := generateKey()
	if err != nil {
		return nil, err
	}
	k := &Key{
		Name:      name,
		Value:     value,
		Priority:  priority,
		CreatedAt: time.Now(),
	}
	s.keys[name] = k
	s.byValue[value] = k
	return k, nil
}

// Revoke removes a key by name.
// Returns ErrKeyNotFound if the name does not exist.
func (s *Store) Revoke(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[name]
	if !ok {
		return ErrKeyNotFound
	}
	delete(s.keys, name)
	delete(s.byValue, k.Value)
	return nil
}

// Update modifies the name and/or priority of an existing key.
// Pass empty string for newName to keep the current name.
// Returns ErrKeyNotFound if the name does not exist.
// Returns ErrKeyExists if newName is already taken by a different key.
func (s *Store) Update(name, newName string, priority int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[name]
	if !ok {
		return ErrKeyNotFound
	}
	if newName == "" {
		newName = name
	}
	if newName != name {
		if _, exists := s.keys[newName]; exists {
			return ErrKeyExists
		}
		delete(s.keys, name)
		k.Name = newName
		s.keys[newName] = k
		s.byValue[k.Value] = k
	}
	k.Priority = priority
	return nil
}

// generateKey returns a 32-byte random hex string.
func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
