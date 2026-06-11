package dedup_test

import (
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/dedup"
)

func TestCache_Disabled_WhenZeroTTL(t *testing.T) {
	c := dedup.NewCache(100, 0)
	if c.Enabled() {
		t.Fatal("expected disabled cache when TTL is 0")
	}
}

func TestCache_Disabled_WhenZeroMaxEntries(t *testing.T) {
	c := dedup.NewCache(0, 10*time.Second)
	if c.Enabled() {
		t.Fatal("expected disabled cache when max entries is 0")
	}
}

func TestCache_GetMiss(t *testing.T) {
	c := dedup.NewCache(100, time.Minute)
	_, ok := c.Get("missing")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestCache_SetGet(t *testing.T) {
	c := dedup.NewCache(100, time.Minute)
	c.Set("key1", []byte(`{"result":"ok"}`))
	v, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected cache hit after Set")
	}
	if string(v) != `{"result":"ok"}` {
		t.Fatalf("unexpected value: %s", v)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c := dedup.NewCache(100, 10*time.Millisecond)
	c.Set("key1", []byte("value"))
	time.Sleep(20 * time.Millisecond)
	_, ok := c.Get("key1")
	if ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestCache_LRUEviction(t *testing.T) {
	c := dedup.NewCache(2, time.Minute)
	c.Set("a", []byte("a"))
	c.Set("b", []byte("b"))
	// Access "a" to make it recently used.
	c.Get("a")
	// Adding "c" should evict "b" (LRU).
	c.Set("c", []byte("c"))

	if _, ok := c.Get("a"); !ok {
		t.Error("expected 'a' to still be in cache")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("expected 'c' to be in cache")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("expected 'b' to be evicted")
	}
}

func TestCache_Stats(t *testing.T) {
	c := dedup.NewCache(100, time.Minute)
	c.Set("k", []byte("v"))
	c.Get("k")    // hit
	c.Get("k")    // hit
	c.Get("nope") // miss

	hits, misses, size := c.Stats()
	if hits != 2 {
		t.Fatalf("expected 2 hits, got %d", hits)
	}
	if misses != 1 {
		t.Fatalf("expected 1 miss, got %d", misses)
	}
	if size != 1 {
		t.Fatalf("expected size 1, got %d", size)
	}
}
