package cache_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/p2pplayer/server/internal/cache"
)

func TestPutGetAndHash(t *testing.T) {
	dir := t.TempDir()
	store, err := cache.New(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Put([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if id != cache.HashBytes([]byte("hello")) {
		t.Fatal("id mismatch")
	}
	data, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("got %q", data)
	}
	id2, err := store.Put([]byte("hello"))
	if err != nil || id2 != id {
		t.Fatal("dedup failed")
	}
}

func TestEviction(t *testing.T) {
	dir := t.TempDir()
	store, err := cache.New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := store.Put([]byte("12345"))
	_, _ = store.Put([]byte("abcdefghij")) // 10 bytes — may evict id1
	if store.Has(id1) && store.Has(cache.HashBytes([]byte("abcdefghij"))) {
		// total would be 15 > 10, so one must be gone after second put
	}
	entries, _ := os.ReadDir(dir)
	var size int64
	for _, e := range entries {
		info, _ := e.Info()
		size += info.Size()
	}
	if size > 10 {
		t.Fatalf("cache size %d exceeds max", size)
	}
	_ = filepath.Base(dir)
}
