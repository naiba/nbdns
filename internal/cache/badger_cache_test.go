package cache

import (
	"github.com/naiba/nbdns/pkg/logger"
	"testing"
)

func TestEmbeddedCacheBoundsCompactionTableGrowth(t *testing.T) {
	cache, err := NewBadgerCache(t.TempDir(), logger.New(false))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	options := cache.db.Opts()
	if options.TableSizeMultiplier != 1 {
		t.Fatalf("compaction tables grow by %d per level; embedded cache requires constant table size", options.TableSizeMultiplier)
	}
	if options.NumMemtables != 2 {
		t.Fatalf("expected two memtables, got %d", options.NumMemtables)
	}
}
