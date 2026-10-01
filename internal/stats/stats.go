package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 每个域名只保留有限数量的高频客户端候选。榜单只展示 Top 10，额外容量用于
// 避免普通流量波动频繁淘汰候选，同时阻止伪造大量客户端地址造成内存无限增长。
const maxClientsPerDomain = 64

const (
	topClientLimit = 10
	topDomainLimit = 50
)

// StatsRecorder 定义统计接口
type StatsRecorder interface {
	RecordQuery()
	RecordDoHQuery()
	RecordCacheHit()
	RecordCacheMiss()
	RecordFailed()
	RecordUpstreamQuery(address string, isError bool)
	RecordClientQuery(clientIP, domain string)
	RecordBlockedQuery(clientIP, domain string)
	GetSnapshot() StatsSnapshot
	Reset()
	Save(dataPath string) error
	Load(dataPath string) error
}

// Stats DNS服务器统计信息
type Stats struct {
	StartTime      time.Time // 应用启动时间（不持久化）
	StatsStartTime time.Time // 统计数据开始时间（可持久化）

	// 查询统计
	TotalQueries  atomic.Uint64
	DoHQueries    atomic.Uint64
	CacheHits     atomic.Uint64
	CacheMisses   atomic.Uint64
	FailedQueries atomic.Uint64

	// 上游服务器统计
	upstreamStats map[string]*UpstreamStats
	mu            sync.RWMutex

	// Top N 统计
	topClients        *TopNTracker // 客户端 IP Top N
	topDomains        *TopNTracker // 已解析域名 Top N
	topBlockedDomains *TopNTracker // 被拦截域名 Top N
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
	now := time.Now()
	return &Stats{
		StartTime:         now,
		StatsStartTime:    now,
		upstreamStats:     make(map[string]*UpstreamStats),
		topClients:        NewTopNTracker(100), // 最多保留 100 个客户端 IP
		topDomains:        NewTopNTracker(200), // 最多保留 200 个域名
		topBlockedDomains: NewTopNTracker(200), // 最多保留 200 个被拦截域名
	}
}

// RecordQuery 记录DNS查询
func (s *Stats) RecordQuery() {
	s.TotalQueries.Add(1)
}

// RecordDoHQuery 记录DoH查询
func (s *Stats) RecordDoHQuery() {
	s.DoHQueries.Add(1)
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
	// 先尝试读锁快速查找
	s.mu.RLock()
	us, ok := s.upstreamStats[address]
	s.mu.RUnlock()

	// 如果不存在才使用写锁创建
	if !ok {
		s.mu.Lock()
		// 双重检查，防止并发创建
		us, ok = s.upstreamStats[address]
		if !ok {
			us = &UpstreamStats{
				Address: address,
			}
			s.upstreamStats[address] = us
		}
		s.mu.Unlock()
	}

	us.TotalQueries.Add(1)
	if isError {
		us.Errors.Add(1)
	}
	us.mu.Lock()
	us.LastUsed = time.Now()
	us.mu.Unlock()
}

// RecordClientQuery 记录一次未被过滤器拦截的客户端查询。
func (s *Stats) RecordClientQuery(clientIP, domain string) {
	if clientIP != "" {
		s.topClients.Record(clientIP, "")
	}
	if domain != "" {
		s.topDomains.Record(domain, clientIP)
	}
}

// RecordBlockedQuery 记录一次被过滤器拦截的查询。
func (s *Stats) RecordBlockedQuery(clientIP, domain string) {
	if clientIP != "" {
		s.topClients.Record(clientIP, "")
	}
	if domain != "" {
		s.topBlockedDomains.Record(domain, clientIP)
	}
}

// Reset 重置统计数据
func (s *Stats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 重置统计开始时间
	s.StatsStartTime = time.Now()

	// 重置查询统计
	s.TotalQueries.Store(0)
	s.DoHQueries.Store(0)
	s.CacheHits.Store(0)
	s.CacheMisses.Store(0)
	s.FailedQueries.Store(0)

	// 重置上游服务器统计
	s.upstreamStats = make(map[string]*UpstreamStats)

	// 重置 Top N 统计
	s.topClients.Reset()
	s.topDomains.Reset()
	s.topBlockedDomains.Reset()
}

// RuntimeStats 运行时统计信息
type RuntimeStats struct {
	Uptime           int64  `json:"uptime"`             // 运行时间（秒）
	UptimeStr        string `json:"uptime_str"`         // 运行时间（可读格式）
	StatsDuration    int64  `json:"stats_duration"`     // 统计时长（秒）
	StatsDurationStr string `json:"stats_duration_str"` // 统计时长（可读格式）
	Goroutines       int    `json:"goroutines"`         // Goroutine数量
	MemAllocMB       uint64 `json:"mem_alloc_mb"`       // 已分配内存（MB）
	MemTotalMB       uint64 `json:"mem_total_mb"`       // 总分配内存（MB）
	MemSysMB         uint64 `json:"mem_sys_mb"`         // 系统内存（MB）
	NumGC            uint32 `json:"num_gc"`             // GC次数
}

// QueryStats 查询统计信息
type QueryStats struct {
	Total       uint64  `json:"total"`        // 总查询数
	DoH         uint64  `json:"doh"`          // DoH查询数
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

// TopNItemJSON Top N 项目（JSON格式）
type TopNItemJSON struct {
	Key            string            `json:"key"`                        // IP 地址或域名
	Count          uint64            `json:"count"`                      // 查询次数
	TopClient      string            `json:"top_client,omitempty"`       // 查询最多的客户端 IP（仅域名统计有）
	TopClientCount uint64            `json:"top_client_count,omitempty"` // 该客户端的查询次数
	TopClients     []ClientCountJSON `json:"top_clients,omitempty"`      // 查询该域名最多的客户端 Top 10
}

// ClientCountJSON 域名下的客户端请求数。
type ClientCountJSON struct {
	Client string `json:"client"`
	Count  uint64 `json:"count"`
}

// StatsSnapshot 完整统计快照
type StatsSnapshot struct {
	Runtime           RuntimeStats        `json:"runtime"`             // 运行时信息
	Queries           QueryStats          `json:"queries"`             // 查询统计
	Upstreams         []UpstreamStatsJSON `json:"upstreams"`           // 上游服务器统计
	TopClients        []TopNItemJSON      `json:"top_clients"`         // Top 客户端 IP
	TopDomains        []TopNItemJSON      `json:"top_domains"`         // Top 已解析域名
	TopBlockedDomains []TopNItemJSON      `json:"top_blocked_domains"` // Top 被拦截域名
}

// GetSnapshot 获取统计快照
func (s *Stats) GetSnapshot() StatsSnapshot {
	// 运行时信息
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	uptime := time.Since(s.StartTime)
	uptimeStr := formatDuration(uptime)

	s.mu.RLock()
	statsStartTime := s.StatsStartTime
	s.mu.RUnlock()
	statsDuration := time.Since(statsStartTime)
	statsDurationStr := formatDuration(statsDuration)

	runtimeStats := RuntimeStats{
		Uptime:           int64(uptime.Seconds()),
		UptimeStr:        uptimeStr,
		StatsDuration:    int64(statsDuration.Seconds()),
		StatsDurationStr: statsDurationStr,
		Goroutines:       runtime.NumGoroutine(),
		MemAllocMB:       m.Alloc / 1024 / 1024,
		MemTotalMB:       m.TotalAlloc / 1024 / 1024,
		MemSysMB:         m.Sys / 1024 / 1024,
		NumGC:            m.NumGC,
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
		DoH:         s.DoHQueries.Load(),
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

	// 按服务器地址字符串排序
	sort.Slice(upstreams, func(i, j int) bool {
		return upstreams[i].Address < upstreams[j].Address
	})

	// Top N 客户端 IP
	topClients := make([]TopNItemJSON, 0)
	for _, item := range s.topClients.GetTopN(topClientLimit) {
		topClients = append(topClients, TopNItemJSON{
			Key:   item.Key,
			Count: item.Count,
		})
	}

	// Top N 已解析及被拦截域名，每个域名附带客户端 Top 10。
	topDomains := topDomainItems(s.topDomains, topDomainLimit)
	topBlockedDomains := topDomainItems(s.topBlockedDomains, topDomainLimit)

	return StatsSnapshot{
		Runtime:           runtimeStats,
		Queries:           queryStats,
		Upstreams:         upstreams,
		TopClients:        topClients,
		TopDomains:        topDomains,
		TopBlockedDomains: topBlockedDomains,
	}
}

func topDomainItems(tracker *TopNTracker, n int) []TopNItemJSON {
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()

	// 只复制候选指针，直接为最终入榜项生成客户端 Top 10，避免先深拷贝
	// 每个域名的完整客户端 map。
	candidates := make([]*TopNItem, 0, len(tracker.items))
	for _, item := range tracker.items {
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Count == candidates[j].Count {
			return candidates[i].Key < candidates[j].Key
		}
		return candidates[i].Count > candidates[j].Count
	})
	if n > len(candidates) {
		n = len(candidates)
	}

	result := make([]TopNItemJSON, 0, n)
	for _, item := range candidates[:n] {
		clients := make([]ClientCountJSON, 0, len(item.clients))
		for client, count := range item.clients {
			clients = append(clients, ClientCountJSON{Client: client, Count: count})
		}
		sort.Slice(clients, func(i, j int) bool {
			if clients[i].Count == clients[j].Count {
				return clients[i].Client < clients[j].Client
			}
			return clients[i].Count > clients[j].Count
		})
		if len(clients) > topClientLimit {
			clients = clients[:topClientLimit]
		}
		entry := TopNItemJSON{Key: item.Key, Count: item.Count, TopClients: clients}
		if len(clients) > 0 {
			entry.TopClient = clients[0].Client
			entry.TopClientCount = clients[0].Count
		}
		result = append(result, entry)
	}
	return result
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

// TopNTracker 追踪 Top N 项目，内存可控
type TopNTracker struct {
	mu       sync.RWMutex
	items    map[string]*TopNItem
	maxItems int // 最大保留项目数
}

// TopNItem Top N 项目统计
type TopNItem struct {
	Key       string
	Count     uint64
	TopClient string            // 对于域名统计，记录查询最多的客户端 IP
	clients   map[string]uint64 // 临时记录客户端分布（仅用于找 Top1）
}

// PersistentStats 持久化统计数据结构
type PersistentStats struct {
	StatsStartTime    time.Time                      `json:"stats_start_time"` // 统计开始时间（可持久化）
	TotalQueries      uint64                         `json:"total_queries"`
	DoHQueries        uint64                         `json:"doh_queries"`
	CacheHits         uint64                         `json:"cache_hits"`
	CacheMisses       uint64                         `json:"cache_misses"`
	FailedQueries     uint64                         `json:"failed_queries"`
	Upstreams         map[string]*PersistentUpstream `json:"upstreams"`
	TopClients        []PersistentTopNItem           `json:"top_clients"`
	TopDomains        []PersistentTopNItem           `json:"top_domains"`
	TopBlockedDomains []PersistentTopNItem           `json:"top_blocked_domains,omitempty"`
}

// PersistentUpstream 持久化上游服务器统计
type PersistentUpstream struct {
	Address      string    `json:"address"`
	TotalQueries uint64    `json:"total_queries"`
	Errors       uint64    `json:"errors"`
	LastUsed     time.Time `json:"last_used"`
}

// PersistentTopNItem 持久化 Top N 项目
type PersistentTopNItem struct {
	Key       string            `json:"key"`
	Count     uint64            `json:"count"`
	TopClient string            `json:"top_client,omitempty"`
	Clients   map[string]uint64 `json:"clients,omitempty"`
}

// NewTopNTracker 创建 Top N 追踪器
func NewTopNTracker(maxItems int) *TopNTracker {
	if maxItems < 1 {
		maxItems = 1
	}
	return &TopNTracker{
		items:    make(map[string]*TopNItem),
		maxItems: maxItems,
	}
}

// Reset 清空追踪器，同时保留追踪器本身，避免并发请求持有失效指针。
func (t *TopNTracker) Reset() {
	t.mu.Lock()
	t.items = make(map[string]*TopNItem)
	t.mu.Unlock()
}

// Record 记录一次访问（可选关联的客户端 IP）
func (t *TopNTracker) Record(key, associatedClient string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	item, exists := t.items[key]
	if !exists {
		// 如果超过最大数量，复用计数最少的项，避免高基数输入导致持续分配。
		if len(t.items) >= t.maxItems {
			item = t.evictLowest()
			item.Key = key
			item.Count = 0
			item.TopClient = ""
			clear(item.clients)
		} else {
			item = &TopNItem{Key: key}
		}
		t.items[key] = item
	}

	item.Count++

	// 如果有关联客户端，记录客户端分布
	if associatedClient != "" {
		if item.clients == nil {
			item.clients = make(map[string]uint64)
		}
		if _, exists := item.clients[associatedClient]; !exists && len(item.clients) >= maxClientsPerDomain {
			item.evictLowestClient()
		}
		item.clients[associatedClient]++
		// 更新 Top1 客户端
		if item.clients[associatedClient] > item.clients[item.TopClient] {
			item.TopClient = associatedClient
		}
	}
}

// evictLowestClient 删除请求数最低的客户端候选（不加锁，由调用者加锁）。
func (item *TopNItem) evictLowestClient() {
	var lowest string
	minCount := ^uint64(0)
	for client, count := range item.clients {
		if count < minCount || (count == minCount && client > lowest) {
			lowest = client
			minCount = count
		}
	}
	if lowest == "" {
		return
	}
	delete(item.clients, lowest)
	if item.TopClient == lowest {
		item.TopClient = ""
		for client, count := range item.clients {
			if count > item.clients[item.TopClient] || (count == item.clients[item.TopClient] && client < item.TopClient) {
				item.TopClient = client
			}
		}
	}
}

// evictLowest 移除并返回计数最少的项（不加锁，由调用者加锁）。
func (t *TopNTracker) evictLowest() *TopNItem {
	var minKey string
	var minCount uint64 = ^uint64(0) // 最大值

	for key, item := range t.items {
		if item.Count < minCount {
			minCount = item.Count
			minKey = key
		}
	}

	item := t.items[minKey]
	delete(t.items, minKey)
	return item
}

// GetTopN 获取 Top N 列表
func (t *TopNTracker) GetTopN(n int) []TopNItem {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// 先只排序轻量指针，确定前 N 后再复制客户端分布。快照通常只需要 10 项，
	// 避免为未入榜的域名分配和复制 map。
	candidates := make([]*TopNItem, 0, len(t.items))
	for _, item := range t.items {
		candidates = append(candidates, item)
	}

	// 按查询次数降序排序
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Count == candidates[j].Count {
			return candidates[i].Key < candidates[j].Key
		}
		return candidates[i].Count > candidates[j].Count
	})

	// 返回前 N 项
	if n > len(candidates) {
		n = len(candidates)
	}
	items := make([]TopNItem, 0, n)
	for _, item := range candidates[:n] {
		clients := make(map[string]uint64, len(item.clients))
		for client, count := range item.clients {
			clients[client] = count
		}
		items = append(items, TopNItem{
			Key:       item.Key,
			Count:     item.Count,
			TopClient: item.TopClient,
			clients:   clients,
		})
	}
	return items
}

// Save 保存统计数据到 JSON 文件
func (s *Stats) Save(dataPath string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 准备持久化数据
	persistent := PersistentStats{
		StatsStartTime:    s.StatsStartTime,
		TotalQueries:      s.TotalQueries.Load(),
		DoHQueries:        s.DoHQueries.Load(),
		CacheHits:         s.CacheHits.Load(),
		CacheMisses:       s.CacheMisses.Load(),
		FailedQueries:     s.FailedQueries.Load(),
		Upstreams:         make(map[string]*PersistentUpstream),
		TopClients:        make([]PersistentTopNItem, 0),
		TopDomains:        make([]PersistentTopNItem, 0),
		TopBlockedDomains: make([]PersistentTopNItem, 0),
	}

	// 保存上游服务器统计
	for addr, us := range s.upstreamStats {
		us.mu.RLock()
		persistent.Upstreams[addr] = &PersistentUpstream{
			Address:      us.Address,
			TotalQueries: us.TotalQueries.Load(),
			Errors:       us.Errors.Load(),
			LastUsed:     us.LastUsed,
		}
		us.mu.RUnlock()
	}

	// 深拷贝追踪器数据，避免序列化期间并发请求修改客户端 map。
	persistent.TopClients = persistentTopNItems(s.topClients)
	persistent.TopDomains = persistentTopNItems(s.topDomains)
	persistent.TopBlockedDomains = persistentTopNItems(s.topBlockedDomains)

	// 序列化为 JSON
	data, err := json.MarshalIndent(persistent, "", "  ")
	if err != nil {
		return err
	}

	// 确保目录存在
	statsPath := filepath.Join(dataPath, "cache")
	if err := os.MkdirAll(statsPath, 0755); err != nil {
		return err
	}

	// 写入文件
	statsFile := filepath.Join(statsPath, "stats.json")
	return os.WriteFile(statsFile, data, 0644)
}

func persistentTopNItems(tracker *TopNTracker) []PersistentTopNItem {
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	items := make([]PersistentTopNItem, 0, len(tracker.items))
	for _, item := range tracker.items {
		clients := make(map[string]uint64, len(item.clients))
		for client, count := range item.clients {
			clients[client] = count
		}
		items = append(items, PersistentTopNItem{
			Key:       item.Key,
			Count:     item.Count,
			TopClient: item.TopClient,
			Clients:   clients,
		})
	}
	return items
}

// Load 从 JSON 文件加载统计数据
func (s *Stats) Load(dataPath string) error {
	statsFile := filepath.Join(dataPath, "cache", "stats.json")

	// 检查文件是否存在
	if _, err := os.Stat(statsFile); os.IsNotExist(err) {
		return nil // 文件不存在不是错误，返回 nil
	}

	// 读取文件
	data, err := os.ReadFile(statsFile)
	if err != nil {
		return err
	}

	// 解析 JSON
	var persistent PersistentStats
	if err := json.Unmarshal(data, &persistent); err != nil {
		return err
	}

	// 恢复统计数据
	s.mu.Lock()
	defer s.mu.Unlock()

	// StartTime 保持为应用启动时间，不从磁盘恢复
	// 只恢复 StatsStartTime（统计数据开始时间）
	s.StatsStartTime = persistent.StatsStartTime
	s.TotalQueries.Store(persistent.TotalQueries)
	s.DoHQueries.Store(persistent.DoHQueries)
	s.CacheHits.Store(persistent.CacheHits)
	s.CacheMisses.Store(persistent.CacheMisses)
	s.FailedQueries.Store(persistent.FailedQueries)

	// 恢复上游服务器统计
	for addr, pus := range persistent.Upstreams {
		us := &UpstreamStats{
			Address:  pus.Address,
			LastUsed: pus.LastUsed,
		}
		us.TotalQueries.Store(pus.TotalQueries)
		us.Errors.Store(pus.Errors)
		s.upstreamStats[addr] = us
	}

	// 恢复 Top 客户端
	s.topClients.mu.Lock()
	for _, pitem := range persistent.TopClients {
		item := &TopNItem{
			Key:     pitem.Key,
			Count:   pitem.Count,
			clients: make(map[string]uint64),
		}
		s.topClients.items[pitem.Key] = item
	}
	s.topClients.mu.Unlock()

	// 恢复 Top 域名
	s.topDomains.mu.Lock()
	for _, pitem := range persistent.TopDomains {
		clients, topClient := restoredClients(pitem)
		item := &TopNItem{
			Key:       pitem.Key,
			Count:     pitem.Count,
			TopClient: topClient,
			clients:   clients,
		}
		s.topDomains.items[pitem.Key] = item
	}
	s.topDomains.mu.Unlock()

	// 恢复 Top 被拦截域名（旧版统计文件没有该字段时保持为空）。
	s.topBlockedDomains.mu.Lock()
	for _, pitem := range persistent.TopBlockedDomains {
		clients, topClient := restoredClients(pitem)
		item := &TopNItem{
			Key:       pitem.Key,
			Count:     pitem.Count,
			TopClient: topClient,
			clients:   clients,
		}
		s.topBlockedDomains.items[pitem.Key] = item
	}
	s.topBlockedDomains.mu.Unlock()

	return nil
}

func restoredClients(item PersistentTopNItem) (map[string]uint64, string) {
	clients := make([]ClientCountJSON, 0, len(item.Clients))
	for client, count := range item.Clients {
		clients = append(clients, ClientCountJSON{Client: client, Count: count})
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Count == clients[j].Count {
			return clients[i].Client < clients[j].Client
		}
		return clients[i].Count > clients[j].Count
	})
	if len(clients) > maxClientsPerDomain {
		clients = clients[:maxClientsPerDomain]
	}
	restored := make(map[string]uint64, len(clients))
	for _, client := range clients {
		restored[client.Client] = client.Count
	}
	topClient := item.TopClient
	if len(clients) > 0 {
		topClient = clients[0].Client
	}
	return restored, topClient
}
