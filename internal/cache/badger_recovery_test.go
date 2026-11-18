package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
)

func cachedTestMessage() *CachedMsg {
	msg := new(dns.Msg).SetQuestion("cached.example.", dns.TypeA)
	return &CachedMsg{Msg: msg, Expires: time.Now().Add(time.Hour)}
}

// A power cut can leave Badger files unreadable. Without recovery the cache
// failed to open on every later start and stayed disabled forever.
func TestBadgerCacheRecoversFromCorruptedFiles(t *testing.T) {
	dataPath := t.TempDir()
	cacheDir := filepath.Join(dataPath, "cache")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"MANIFEST":    "not a badger manifest",
		"000001.vlog": "garbage",
		"000002.sst":  "garbage",
		"stats.json":  `{"total_queries":42}`, // Shares the directory; must survive.
	} {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := NewBadgerCache(dataPath, logger.New(false))
	if err != nil {
		t.Fatalf("corrupted cache files must be discarded, not disable the cache: %v", err)
	}
	defer cache.Close()
	if err := cache.Set("key", cachedTestMessage(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get("key"); !ok {
		t.Error("recovered cache does not work")
	}
	if data, err := os.ReadFile(filepath.Join(cacheDir, "stats.json")); err != nil || string(data) != `{"total_queries":42}` {
		t.Errorf("stats.json damaged by recovery: %q, %v", data, err)
	}
}

// A database held by another process is not corrupted and must never be wiped.
func TestBadgerCacheNeverWipesDatabaseInUse(t *testing.T) {
	dataPath := t.TempDir()
	first, err := NewBadgerCache(dataPath, logger.New(false))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := first.Set("key", cachedTestMessage(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if second, err := NewBadgerCache(dataPath, logger.New(false)); err == nil {
		_ = second.Close()
		t.Fatal("second open of a locked database must fail")
	}
	if _, ok := first.Get("key"); !ok {
		t.Error("data of the running instance was wiped")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewBadgerCache(dataPath, logger.New(false))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok := reopened.Get("key"); !ok {
		t.Error("persisted entry lost after a refused second open")
	}
}
