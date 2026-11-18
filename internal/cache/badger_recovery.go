package cache

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// badgerInUseMessage appears in Badger's directory lock errors on every platform.
const badgerInUseMessage = "Another process is using this Badger database"

// openBadgerRecovering opens the cache and, if its files are unreadable (for
// example after a power cut), discards them once and starts empty. Without this
// the cache failed to open on every later start and stayed disabled forever.
// It never touches a database another process holds.
func openBadgerRecovering(opts badger.Options) (*badger.DB, error) {
	db, err := badger.Open(opts)
	if err == nil || strings.Contains(err.Error(), badgerInUseMessage) {
		return db, err
	}
	// Standard log, not the debug logger: losing the cache must be visible.
	log.Printf("BadgerDB cache is unreadable (%v); discarding cache files and starting empty", err)
	if removeErr := removeBadgerFiles(opts.Dir); removeErr != nil {
		return nil, err
	}
	return badger.Open(opts)
}

// removeBadgerFiles deletes only files Badger owns; the directory also holds
// stats.json, which must survive.
func removeBadgerFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !isBadgerFile(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isBadgerFile(name string) bool {
	switch name {
	case "MANIFEST", "MANIFEST-REWRITE", "KEYREGISTRY", "REWRITE-KEYREGISTRY", "DISCARD", "LOCK":
		return true
	}
	switch filepath.Ext(name) {
	case ".sst", ".vlog", ".mem":
		return true
	}
	return false
}
