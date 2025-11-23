package cache

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
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

// NewBadgerCache creates a new BadgerDB cache instance with optimized settings for embedded devices
func NewBadgerCache(dataPath string, log logger.Logger) (*BadgerCache, error) {
	dbPath := filepath.Join(dataPath, "cache")

	opts := badger.DefaultOptions(dbPath)

	// 针对家用路由器等嵌入式设备的优化配置
	// MemTable：减少到 4MB，降低内存占用
	opts.MemTableSize = 4 << 20 // 4MB

	// ValueLog：减少到 4MB，减少磁盘 I/O 和存储占用
	opts.ValueLogFileSize = 4 << 20 // 4MB

	// BlockCache：减少到 16MB，大幅降低内存使用
	opts.BlockCacheSize = 16 << 20 // 16MB

	// IndexCache：限制索引缓存大小为 8MB
	opts.IndexCacheSize = 8 << 20

	// Level 0 tables：减少数量以降低内存占用
	opts.NumLevelZeroTables = 2
	opts.NumLevelZeroTablesStall = 4

	// 压缩选项：降低压缩等级以减少 CPU 使用
	opts.Compression = options.None // 关闭压缩，节省 CPU（DNS 响应本身较小）

	// ValueThreshold：降低到 512 字节，更多数据内联存储
	// DNS 响应通常小于 512 字节，内联存储可以减少磁盘访问
	opts.ValueThreshold = 512

	// 禁用同步写入以提高性能，DNS 缓存丢失可接受
	opts.SyncWrites = false

	// 减少 ValueLog 条目数量
	opts.ValueLogMaxEntries = 50000 // 50k

	// 限制并发压缩数量，降低 CPU 和 I/O 压力
	opts.NumCompactors = 1

	// 禁用检测冲突，减少内存开销
	opts.DetectConflicts = false

	// 设置日志级别为 ERROR，减少日志开销
	opts.Logger = nil

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	cache := &BadgerCache{db: db, logger: log}

	// Start garbage collection routines
	go cache.runGC()
	go cache.runCompaction()

	return cache, nil
}

// Set stores a DNS message in the cache with the given key and TTL
func (bc *BadgerCache) Set(key string, msg *CachedMsg, ttl time.Duration) error {
	// Pack DNS message to wire format
	dnsData, err := msg.Msg.Pack()
	if err != nil {
		return fmt.Errorf("failed to pack DNS message: %w", err)
	}

	// 直接存储二进制数据：8字节过期时间 + DNS wire format
	// 避免 JSON 序列化开销
	expiresBytes := make([]byte, 8)
	// 使用 Unix 时间戳（秒）
	expiresUnix := msg.Expires.Unix()
	for i := 0; i < 8; i++ {
		expiresBytes[i] = byte(expiresUnix >> (56 - i*8))
	}

	// 组合数据：过期时间 + DNS数据
	data := append(expiresBytes, dnsData...)

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
			// 数据格式：8字节过期时间 + DNS wire format
			if len(val) < 8 {
				return fmt.Errorf("invalid cache data: too short")
			}

			// 解析过期时间
			var expiresUnix int64
			for i := 0; i < 8; i++ {
				expiresUnix = (expiresUnix << 8) | int64(val[i])
			}
			expires := time.Unix(expiresUnix, 0)

			// 解析 DNS 消息
			msg := new(dns.Msg)
			if err := msg.Unpack(val[8:]); err != nil {
				return fmt.Errorf("failed to unpack DNS message: %w", err)
			}

			cachedMsg = &CachedMsg{
				Msg:     msg,
				Expires: expires,
			}
			return nil
		})
	})

	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, false
		}
		bc.logger.Printf("Cache get error for key %s: %v", key, err)
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

// runGC runs garbage collection periodically to clean up expired entries in value log
func (bc *BadgerCache) runGC() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		// Run GC multiple times until no more rewrite is needed
		gcCount := 0
		for {
			err := bc.db.RunValueLogGC(0.5)
			if err != nil {
				if err != badger.ErrNoRewrite {
					bc.logger.Printf("BadgerDB GC error: %v", err)
				}
				break
			}
			gcCount++
			// Limit GC runs and add delay to prevent CPU hogging
			if gcCount >= 10 {
				bc.logger.Printf("BadgerDB GC: reached max runs limit (10)")
				break
			}
			// Sleep briefly between GC cycles to reduce CPU usage
			time.Sleep(500 * time.Millisecond)
		}

		if gcCount > 0 {
			bc.logger.Printf("BadgerDB GC: completed %d runs", gcCount)
		}

		// Check disk usage and clean if necessary
		bc.checkAndCleanDiskUsage()
	}
}

// runCompaction runs LSM tree compaction periodically to clean up expired key metadata
func (bc *BadgerCache) runCompaction() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		err := bc.db.Flatten(1)
		if err != nil {
			bc.logger.Printf("BadgerDB compaction error: %v", err)
		}
	}
}

// checkAndCleanDiskUsage checks if cache exceeds size limit and triggers cleanup
func (bc *BadgerCache) checkAndCleanDiskUsage() {
	lsm, vlog := bc.db.Size()
	totalSize := lsm + vlog
	maxSize := int64(50 << 20) // 50MB limit (适合家用路由器等嵌入式设备)

	if totalSize > maxSize {
		bc.logger.Printf("Cache size %d MB exceeds limit %d MB, triggering cleanup", totalSize>>20, maxSize>>20)
		// Force compaction to reduce size
		if err := bc.db.Flatten(2); err != nil {
			bc.logger.Printf("BadgerDB flatten error: %v", err)
		}
	}
}

// Stats returns cache statistics
func (bc *BadgerCache) Stats() string {
	lsm, vlog := bc.db.Size()
	return fmt.Sprintf("LSM size: %d bytes, Value log size: %d bytes", lsm, vlog)
}
