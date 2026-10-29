package web

import (
	"bytes"
	"testing"
)

func TestResponseCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newResponseCache(2)
	cache.put("a", []byte("1"))
	cache.put("b", []byte("2"))

	if _, ok := cache.get("a"); !ok {
		t.Fatal("cache lost the key a")
	}
	cache.put("c", []byte("3"))

	if _, ok := cache.get("b"); ok {
		t.Error("cache kept the least recently used key b")
	}
	for key, want := range map[string]string{"a": "1", "c": "3"} {
		body, ok := cache.get(key)
		if !ok || !bytes.Equal(body, []byte(want)) {
			t.Errorf("get(%s) = %q, %v; want %q, true", key, body, ok, want)
		}
	}
}

func TestResponseCacheReplacesAndCounts(t *testing.T) {
	cache := newResponseCache(4)
	if _, ok := cache.get("missing"); ok {
		t.Fatal("get on an empty cache reported a hit")
	}
	cache.put("k", []byte("old"))
	cache.put("k", []byte("new"))
	if body, ok := cache.get("k"); !ok || !bytes.Equal(body, []byte("new")) {
		t.Fatalf("get(k) = %q, %v; want new, true", body, ok)
	}
	if cache.hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", cache.hits.Load())
	}
	if cache.misses.Load() != 1 {
		t.Errorf("misses = %d, want 1", cache.misses.Load())
	}
}

func TestCacheKeyDependsOnEndpointAndData(t *testing.T) {
	check := cacheKey("/pic/check", 1, []byte("image"))
	if check == cacheKey("/pic/labels", 1, []byte("image")) {
		t.Error("cache key ignores the endpoint")
	}
	if check == cacheKey("/pic/check", 1, []byte("other")) {
		t.Error("cache key ignores the data")
	}
	if check == cacheKey("/pic/check", 2, []byte("image")) {
		t.Error("cache key ignores the sensitivity")
	}
	if check != cacheKey("/pic/check", 1, []byte("image")) {
		t.Error("cache key is not stable")
	}
}
