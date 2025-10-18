package cache

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
)

// Cache 定义缓存接口
type Cache interface {
	Get(key string) (*CachedMsg, bool)
	Set(key string, msg *CachedMsg, ttl time.Duration) error
	Delete(key string) error
	Close() error
	Stats() string
}

// CachedMsg represents a cached DNS message with expiration time
type CachedMsg struct {
	Msg     *dns.Msg  `json:"msg"`
	Expires time.Time `json:"expires"`
}

// BadgerCache wraps BadgerDB for DNS query caching
type BadgerCache struct {
	db     *badger.DB
	logger logger.Logger
}

// NewBadgerCache creates a new BadgerDB cache instance with a 40MB size limit
func NewBadgerCache(dataPath string, log logger.Logger) (*BadgerCache, error) {
	dbPath := filepath.Join(dataPath, "cache")

	opts := badger.DefaultOptions(dbPath)
	// Set memory table size to ~8MB to keep memory usage reasonable
	opts.MemTableSize = 8 << 20 // 8MB
	// Set value log file size to ~8MB
	opts.ValueLogFileSize = 8 << 20 // 8MB
	// Set maximum cache size to 40MB total
	opts.BlockCacheSize = 32 << 20 // 32MB for block cache
	// Reduce number of levels to optimize for smaller database
	opts.NumLevelZeroTables = 2
	opts.NumLevelZeroTablesStall = 4
	// Enable value log to reduce memory usage
	opts.ValueThreshold = 1024 // Values larger than 1KB go to value log
	// Set sync writes to false for better performance (data loss risk on crash)
	opts.SyncWrites = false

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	cache := &BadgerCache{db: db, logger: log}

	// Start garbage collection routine
	go cache.runGC()

	return cache, nil
}

// Set stores a DNS message in the cache with the given key and TTL
func (bc *BadgerCache) Set(key string, msg *CachedMsg, ttl time.Duration) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal cached message: %w", err)
	}

	return bc.db.Update(func(txn *badger.Txn) error {
		entry := badger.NewEntry([]byte(key), data).WithTTL(ttl)
		return txn.SetEntry(entry)
	})
}

// Get retrieves a DNS message from the cache
func (bc *BadgerCache) Get(key string) (*CachedMsg, bool) {
	var cachedMsg *CachedMsg

	err := bc.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}

		return item.Value(func(val []byte) error {
			cachedMsg = &CachedMsg{}
			return json.Unmarshal(val, cachedMsg)
		})
	})

	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, false
		}
		bc.logger.Printf("Cache get error: %v", err)
		return nil, false
	}

	// Check if the cached message has expired
	if time.Now().After(cachedMsg.Expires) {
		// Delete expired entry
		bc.Delete(key)
		return nil, false
	}

	return cachedMsg, true
}

// Delete removes a key from the cache
func (bc *BadgerCache) Delete(key string) error {
	return bc.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// Close closes the BadgerDB instance
func (bc *BadgerCache) Close() error {
	return bc.db.Close()
}

// runGC runs garbage collection periodically to clean up expired entries
func (bc *BadgerCache) runGC() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		err := bc.db.RunValueLogGC(0.5)
		if err != nil && err != badger.ErrNoRewrite {
			bc.logger.Printf("BadgerDB GC error: %v", err)
		}
	}
}

// Stats returns cache statistics
func (bc *BadgerCache) Stats() string {
	lsm, vlog := bc.db.Size()
	return fmt.Sprintf("LSM size: %d bytes, Value log size: %d bytes", lsm, vlog)
}
