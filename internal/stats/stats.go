package stats

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// StatsRecorder 定义统计接口
type StatsRecorder interface {
	RecordQuery()
	RecordCacheHit()
	RecordCacheMiss()
	RecordFailed()
	RecordUpstreamQuery(address string, isError bool)
	GetSnapshot() StatsSnapshot
}

// Stats DNS服务器统计信息
type Stats struct {
	StartTime time.Time

	// 查询统计
	TotalQueries   atomic.Uint64
	CacheHits      atomic.Uint64
	CacheMisses    atomic.Uint64
	FailedQueries  atomic.Uint64

	// 上游服务器统计
	upstreamStats map[string]*UpstreamStats
	mu            sync.RWMutex
}

// UpstreamStats 上游服务器统计
type UpstreamStats struct {
	Address      string
	TotalQueries atomic.Uint64
	Errors       atomic.Uint64
	LastUsed     time.Time
	mu           sync.RWMutex
}

// NewStats 创建统计实例
func NewStats() *Stats {
	return &Stats{
		StartTime:     time.Now(),
		upstreamStats: make(map[string]*UpstreamStats),
	}
}

// RecordQuery 记录DNS查询
func (s *Stats) RecordQuery() {
	s.TotalQueries.Add(1)
}

// RecordCacheHit 记录缓存命中
func (s *Stats) RecordCacheHit() {
	s.CacheHits.Add(1)
}

// RecordCacheMiss 记录缓存未命中
func (s *Stats) RecordCacheMiss() {
	s.CacheMisses.Add(1)
}

// RecordFailed 记录查询失败
func (s *Stats) RecordFailed() {
	s.FailedQueries.Add(1)
}

// RecordUpstreamQuery 记录上游服务器查询
func (s *Stats) RecordUpstreamQuery(address string, isError bool) {
	s.mu.Lock()
	us, ok := s.upstreamStats[address]
	if !ok {
		us = &UpstreamStats{
			Address: address,
		}
		s.upstreamStats[address] = us
	}
	s.mu.Unlock()

	us.TotalQueries.Add(1)
	if isError {
		us.Errors.Add(1)
	}
	us.mu.Lock()
	us.LastUsed = time.Now()
	us.mu.Unlock()
}

// RuntimeStats 运行时统计信息
type RuntimeStats struct {
	Uptime       int64  `json:"uptime"`        // 运行时间（秒）
	UptimeStr    string `json:"uptime_str"`    // 运行时间（可读格式）
	Goroutines   int    `json:"goroutines"`    // Goroutine数量
	MemAllocMB   uint64 `json:"mem_alloc_mb"`  // 已分配内存（MB）
	MemTotalMB   uint64 `json:"mem_total_mb"`  // 总分配内存（MB）
	MemSysMB     uint64 `json:"mem_sys_mb"`    // 系统内存（MB）
	NumGC        uint32 `json:"num_gc"`        // GC次数
}

// QueryStats 查询统计信息
type QueryStats struct {
	Total       uint64  `json:"total"`        // 总查询数
	CacheHits   uint64  `json:"cache_hits"`   // 缓存命中数
	CacheMisses uint64  `json:"cache_misses"` // 缓存未命中数
	Failed      uint64  `json:"failed"`       // 失败查询数
	HitRate     float64 `json:"hit_rate"`     // 缓存命中率
}

// UpstreamStatsJSON 上游服务器统计（JSON格式）
type UpstreamStatsJSON struct {
	Address      string  `json:"address"`       // 服务器地址
	TotalQueries uint64  `json:"total_queries"` // 总查询数
	Errors       uint64  `json:"errors"`        // 错误数
	ErrorRate    float64 `json:"error_rate"`    // 错误率
	LastUsed     string  `json:"last_used"`     // 最后使用时间
}

// StatsSnapshot 完整统计快照
type StatsSnapshot struct {
	Runtime   RuntimeStats        `json:"runtime"`   // 运行时信息
	Queries   QueryStats          `json:"queries"`   // 查询统计
	Upstreams []UpstreamStatsJSON `json:"upstreams"` // 上游服务器统计
}

// GetSnapshot 获取统计快照
func (s *Stats) GetSnapshot() StatsSnapshot {
	// 运行时信息
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	uptime := time.Since(s.StartTime)
	uptimeStr := formatDuration(uptime)

	runtimeStats := RuntimeStats{
		Uptime:       int64(uptime.Seconds()),
		UptimeStr:    uptimeStr,
		Goroutines:   runtime.NumGoroutine(),
		MemAllocMB:   m.Alloc / 1024 / 1024,
		MemTotalMB:   m.TotalAlloc / 1024 / 1024,
		MemSysMB:     m.Sys / 1024 / 1024,
		NumGC:        m.NumGC,
	}

	// 查询统计
	total := s.TotalQueries.Load()
	hits := s.CacheHits.Load()
	misses := s.CacheMisses.Load()
	failed := s.FailedQueries.Load()

	var hitRate float64
	if total > 0 {
		hitRate = float64(hits) / float64(total) * 100
	}

	queryStats := QueryStats{
		Total:       total,
		CacheHits:   hits,
		CacheMisses: misses,
		Failed:      failed,
		HitRate:     hitRate,
	}

	// 上游服务器统计
	s.mu.RLock()
	upstreams := make([]UpstreamStatsJSON, 0, len(s.upstreamStats))
	for _, us := range s.upstreamStats {
		queries := us.TotalQueries.Load()
		errors := us.Errors.Load()
		var errorRate float64
		if queries > 0 {
			errorRate = float64(errors) / float64(queries) * 100
		}

		us.mu.RLock()
		lastUsed := us.LastUsed.Format("2006-01-02 15:04:05")
		if us.LastUsed.IsZero() {
			lastUsed = "Never"
		}
		us.mu.RUnlock()

		upstreams = append(upstreams, UpstreamStatsJSON{
			Address:      us.Address,
			TotalQueries: queries,
			Errors:       errors,
			ErrorRate:    errorRate,
			LastUsed:     lastUsed,
		})
	}
	s.mu.RUnlock()

	return StatsSnapshot{
		Runtime:   runtimeStats,
		Queries:   queryStats,
		Upstreams: upstreams,
	}
}

// formatDuration 格式化时长为可读格式
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return formatString("%d天%d小时%d分钟", days, hours, minutes)
	} else if hours > 0 {
		return formatString("%d小时%d分钟%d秒", hours, minutes, seconds)
	} else if minutes > 0 {
		return formatString("%d分钟%d秒", minutes, seconds)
	}
	return formatString("%d秒", seconds)
}

// formatString 简单的字符串格式化
func formatString(format string, args ...interface{}) string {
	result := format
	for _, arg := range args {
		switch v := arg.(type) {
		case int:
			result = replaceFirst(result, "%d", itoa(v))
		}
	}
	return result
}

// replaceFirst 替换第一个匹配的字符串
func replaceFirst(s, old, new string) string {
	for i := 0; i <= len(s)-len(old); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

// itoa 整数转字符串
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	var buf [32]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
