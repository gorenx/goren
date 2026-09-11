package local

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCacheAtomicReplacementBoundsAndIntegrity(t *testing.T) {
	ctx := context.Background()
	storage, err := NewObjects(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := string(contentID([]byte("transform descriptor")))
	if _, err := storage.ReadCache(ctx, key, 1024); err == nil {
		t.Fatal("missing cache accepted")
	}
	var workers sync.WaitGroup
	for n := 0; n < 12; n++ {
		workers.Go(func() {
			payload := bytes.Repeat([]byte{byte(n)}, 1024)
			if err := storage.WriteCache(ctx, key, payload); err != nil {
				t.Error(err)
				return
			}
			got, err := storage.ReadCache(ctx, key, 1024)
			if err != nil {
				t.Error(err)
				return
			}
			if len(got) != 1024 || !bytes.Equal(got, bytes.Repeat(got[:1], 1024)) {
				t.Error("partial concurrent cache entry")
			}
		})
	}
	workers.Wait()
	if _, err := storage.ReadCache(ctx, key, 1023); err == nil {
		t.Fatal("oversized cache accepted")
	}
	path, err := storage.cachePath(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ReadCache(ctx, key, 1024); err == nil {
		t.Fatal("checksum damage accepted")
	}
	if err := storage.WriteCache(ctx, key, []byte("rebuilt")); err != nil {
		t.Fatal(err)
	}
	got, err := storage.ReadCache(ctx, key, 1024)
	if err != nil || string(got) != "rebuilt" {
		t.Fatalf("rebuild: %q, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging cleanup: %v, %v", entries, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := storage.WriteCache(canceled, key, []byte("replacement")); err != context.Canceled {
		t.Fatalf("canceled write: %v", err)
	}
	got, err = storage.ReadCache(ctx, key, 1024)
	if err != nil || string(got) != "rebuilt" {
		t.Fatal("canceled write changed cache")
	}
	if _, err := storage.ReadCache(canceled, key, 1024); err != context.Canceled {
		t.Fatalf("canceled read: %v", err)
	}
	if err := storage.WriteCache(ctx, "../../escape", nil); err == nil {
		t.Fatal("invalid cache identity accepted")
	}
}
