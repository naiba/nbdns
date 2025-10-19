package handler

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/cache"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

type Handler struct {
	strategy                          int
	commonUpstreams, specialUpstreams []*model.Upstream
	builtInCache                      cache.Cache
	logger                            logger.Logger
	stats                             stats.StatsRecorder
}

func NewHandler(strategy int, builtInCache bool,
	upstreams []*model.Upstream,
	dataPath string,
	log logger.Logger,
	statsRecorder stats.StatsRecorder) *Handler {
	var c cache.Cache
	if builtInCache {
		var err error
		c, err = cache.NewBadgerCache(dataPath, log)
		if err != nil {
			log.Printf("Failed to initialize BadgerDB cache: %v", err)
			log.Printf("Cache will be disabled")
			c = nil
		} else {
			log.Printf("BadgerDB cache initialized successfully at %s", dataPath)
		}
	}
	var commonUpstreams, specialUpstreams []*model.Upstream
	for i := 0; i < len(upstreams); i++ {
		if len(upstreams[i].Match) > 0 {
			specialUpstreams = append(specialUpstreams, upstreams[i])
		} else {
			commonUpstreams = append(commonUpstreams, upstreams[i])
		}
	}
	return &Handler{
		strategy:         strategy,
		commonUpstreams:  commonUpstreams,
		specialUpstreams: specialUpstreams,
		builtInCache:     c,
		logger:           log,
		stats:            statsRecorder,
	}
}

func (h *Handler) matchedUpstreams(req *dns.Msg) []*model.Upstream {
	if len(req.Question) == 0 {
		return h.commonUpstreams
	}
	q := req.Question[0]
	var matchedUpstreams []*model.Upstream
	for i := 0; i < len(h.specialUpstreams); i++ {
		if h.specialUpstreams[i].IsMatch(q.Name) {
			matchedUpstreams = append(matchedUpstreams, h.specialUpstreams[i])
		}
	}
	if len(matchedUpstreams) > 0 {
		return matchedUpstreams
	}
	return h.commonUpstreams
}

func (h *Handler) LookupIP(host string) (ip net.IP, err error) {
	if ip = net.ParseIP(host); ip != nil {
		return ip, nil
	}
	if !strings.HasSuffix(host, ".") {
		host += "."
	}
	m := new(dns.Msg)
	m.Id = dns.Id()
	m.RecursionDesired = true
	m.Question = make([]dns.Question, 1)
	m.Question[0] = dns.Question{Name: host, Qtype: dns.TypeA, Qclass: dns.ClassINET}
	res := h.exchange(m)
	// 取一个 IPv4 地址
	for i := 0; i < len(res.Answer); i++ {
		if aRecord, ok := res.Answer[i].(*dns.A); ok {
			ip = aRecord.A
		}
	}
	// 选取最后一个（一般是备用，存活率高一些）
	if ip == nil {
		err = errors.New("no ipv4 address found")
	}

	h.logger.Printf("bootstrap LookupIP: %s %v --> %s %v", host, res.Answer, ip, err)
	return
}

// removeEDNS 清理请求中的 EDNS 客户端子网信息
func (h *Handler) removeEDNS(req *dns.Msg) {
	opt := req.IsEdns0()
	if opt == nil {
		return
	}

	// 过滤掉 EDNS Client Subnet 选项
	var newOptions []dns.EDNS0
	for _, option := range opt.Option {
		if _, ok := option.(*dns.EDNS0_SUBNET); !ok {
			// 保留非 ECS 的其他选项
			newOptions = append(newOptions, option)
		} else {
			h.logger.Printf("Removed EDNS Client Subnet from request")
		}
	}
	opt.Option = newOptions
}

func (h *Handler) exchange(req *dns.Msg) *dns.Msg {
	// 清理 EDNS 客户端子网信息
	h.removeEDNS(req)

	var msgs []*dns.Msg

	switch h.strategy {
	case model.StrategyFullest:
		msgs = h.getTheFullestResults(req)
	case model.StrategyFastest:
		msgs = h.getTheFastestResults(req)
	case model.StrategyAnyResult:
		msgs = h.getAnyResult(req)
	}

	var res *dns.Msg

	for i := 0; i < len(msgs); i++ {
		if msgs[i] == nil {
			continue
		}
		if res == nil {
			res = msgs[i]
			continue
		}
		res.Answer = append(res.Answer, msgs[i].Answer...)
	}

	if res == nil {
		// 如果全部上游挂了要返回错误
		res = new(dns.Msg)
		res.Rcode = dns.RcodeServerFailure
	} else {
		res.Answer = uniqueAnswer(res.Answer)
	}

	return res
}

func getDnsRequestCacheKey(m *dns.Msg) string {
	var dnssec string
	if o := m.IsEdns0(); o != nil {
		// 区分 DNSSEC 请求，避免将非 DNSSEC 响应返回给需要 DNSSEC 的客户端
		if o.Do() {
			dnssec = "DO"
		}
		// 服务多区域的公共dns使用
		// for _, s := range o.Option {
		// 	switch e := s.(type) {
		// 	case *dns.EDNS0_SUBNET:
		// 		edns = e.Address.String()
		// 	}
		// }
	}
	return fmt.Sprintf("%s#%d#%s", model.GetDomainNameFromDnsMsg(m), m.Question[0].Qtype, dnssec)
}

func getDnsResponseTtl(m *dns.Msg) time.Duration {
	var ttl uint32
	if len(m.Answer) > 0 {
		ttl = m.Answer[0].Header().Ttl
	}
	if ttl < 60 {
		ttl = 60 // 最小 ttl 1 分钟
	} else if ttl > 3600 {
		ttl = 3600 // 最大 ttl 1 小时
	}
	return time.Duration(ttl) * time.Second
}

// shouldCacheResponse 判断响应是否应该被缓存
func shouldCacheResponse(m *dns.Msg) bool {
	// 不缓存服务器错误响应
	if m.Rcode == dns.RcodeServerFailure {
		return false
	}

	// 不缓存格式错误的响应
	if m.Rcode == dns.RcodeFormatError {
		return false
	}

	// NXDOMAIN (域名不存在) 可以缓存，但时间较短（由 getDnsResponseTtl 控制）
	// NOERROR 和 NXDOMAIN 都可以缓存
	return m.Rcode == dns.RcodeSuccess || m.Rcode == dns.RcodeNameError
}

// validateResponse 验证 DNS 响应，防止缓存投毒
// 返回 true 表示响应有效，false 表示可能存在投毒风险
func validateResponse(req *dns.Msg, resp *dns.Msg, debugLogger logger.Logger) bool {
	// 1. 检查响应是否为空
	if resp == nil {
		return false
	}

	// 2. 检查请求和响应的问题数量
	if len(req.Question) == 0 || len(resp.Question) == 0 {
		return true // 如果没有问题部分，跳过验证（某些响应可能没有问题部分）
	}

	// 3. 验证域名匹配（不区分大小写）
	if !strings.EqualFold(req.Question[0].Name, resp.Question[0].Name) {
		debugLogger.Printf("DNS response validation failed: domain mismatch - request: %s, response: %s",
			req.Question[0].Name, resp.Question[0].Name)
		return false
	}

	// 4. 验证查询类型匹配
	if req.Question[0].Qtype != resp.Question[0].Qtype {
		debugLogger.Printf("DNS response validation failed: qtype mismatch - request: %d, response: %d",
			req.Question[0].Qtype, resp.Question[0].Qtype)
		return false
	}

	// 5. 验证查询类别匹配（通常都是 IN - Internet）
	if req.Question[0].Qclass != resp.Question[0].Qclass {
		debugLogger.Printf("DNS response validation failed: qclass mismatch - request: %d, response: %d",
			req.Question[0].Qclass, resp.Question[0].Qclass)
		return false
	}

	// 6. 验证 Answer 部分的域名（防止返回无关域名的记录）
	requestDomain := strings.ToLower(strings.TrimSuffix(req.Question[0].Name, "."))
	validDomains := make(map[string]bool)
	validDomains[requestDomain] = true

	// 第一遍：收集所有 CNAME 目标域名
	for _, answer := range resp.Answer {
		if answer.Header().Rrtype == dns.TypeCNAME {
			if cname, ok := answer.(*dns.CNAME); ok {
				cnameTarget := strings.ToLower(strings.TrimSuffix(cname.Target, "."))
				validDomains[cnameTarget] = true
			}
		}
	}

	// 第二遍：验证所有应答记录
	for _, answer := range resp.Answer {
		answerDomain := strings.ToLower(strings.TrimSuffix(answer.Header().Name, "."))

		// 检查应答记录的域名是否在有效域名列表中
		if !validDomains[answerDomain] {
			// 对于 CNAME 记录，域名必须是请求域名
			if answer.Header().Rrtype == dns.TypeCNAME {
				if answerDomain != requestDomain {
					debugLogger.Printf("DNS response validation failed: CNAME domain mismatch - request: %s, CNAME: %s",
						requestDomain, answerDomain)
					return false
				}
			} else {
				// 对于其他记录类型，记录警告但不拒绝（某些服务器可能返回额外记录）
				debugLogger.Printf("DNS response validation warning: answer domain not in valid chain - request: %s, answer: %s (type: %d)",
					requestDomain, answerDomain, answer.Header().Rrtype)
			}
		}
	}

	// 7. 检查 TTL 值的合理性（防止异常的 TTL 值）
	for _, answer := range resp.Answer {
		ttl := answer.Header().Ttl
		// TTL 不应该超过 7 天（604800 秒）
		if ttl > 604800 {
			debugLogger.Printf("DNS response validation warning: suspiciously high TTL: %d seconds for %s",
				ttl, answer.Header().Name)
		}
	}

	return true
}

// HandleDnsMsg 处理 DNS 查询的核心逻辑（支持缓存和统计）
// clientIP 和 domain 用于统计，如果为空则自动从请求中提取 domain
func (h *Handler) HandleDnsMsg(req *dns.Msg, clientIP, domain string) *dns.Msg {
	h.logger.Printf("nbdns::request %+v\n", req)

	// 记录查询统计
	if h.stats != nil {
		h.stats.RecordQuery()

		// 提取域名（如果未提供）
		if domain == "" && len(req.Question) > 0 {
			domain = req.Question[0].Name
		}

		// 记录客户端查询
		if clientIP != "" || domain != "" {
			h.stats.RecordClientQuery(clientIP, domain)
		}
	}

	// 检查缓存
	var cacheKey string
	if h.builtInCache != nil {
		cacheKey = getDnsRequestCacheKey(req)
		if v, ok := h.builtInCache.Get(cacheKey); ok {
			// 记录缓存命中
			if h.stats != nil {
				h.stats.RecordCacheHit()
			}

			resp := v.Msg.Copy()
			// 更新缓存的 answer 的 TTL
			for i := 0; i < len(resp.Answer); i++ {
				header := resp.Answer[i].Header()
				if header == nil {
					continue
				}
				header.Ttl = uint32(time.Until(v.Expires).Seconds())
			}
			resp.SetReply(req)
			return resp
		}
		// 记录缓存未命中
		if h.stats != nil {
			h.stats.RecordCacheMiss()
		}
	}

	// 从上游获取响应
	resp := h.exchange(req)

	// 记录失败查询
	if resp.Rcode == dns.RcodeServerFailure && h.stats != nil {
		h.stats.RecordFailed()
	}

	resp.SetReply(req)
	h.logger.Printf("nbdns::resp: %+v\n", resp)

	// 验证响应并缓存（防止缓存投毒）
	if h.builtInCache != nil && shouldCacheResponse(resp) && validateResponse(req, resp, h.logger) {
		ttl := getDnsResponseTtl(resp)
		cachedMsg := &cache.CachedMsg{
			Msg:     resp,
			Expires: time.Now().Add(ttl),
		}
		if err := h.builtInCache.Set(cacheKey, cachedMsg, ttl); err != nil {
			h.logger.Printf("Failed to cache response: %v", err)
		}
	}

	return resp
}

// extractClientIPFromDNS 从 DNS 请求中提取客户端 IP
// 优先级：EDNS Client Subnet > RemoteAddr
func extractClientIPFromDNS(w dns.ResponseWriter, req *dns.Msg) string {
	// 1. 优先检查 EDNS Client Subnet (ECS)
	// ECS 是 DNS 协议标准，用于传递真实客户端 IP
	if opt := req.IsEdns0(); opt != nil {
		for _, option := range opt.Option {
			if ecs, ok := option.(*dns.EDNS0_SUBNET); ok {
				// ECS 中的 Address 就是客户端真实 IP
				return ecs.Address.String()
			}
		}
	}

	// 2. 从 RemoteAddr 获取
	var clientIP string
	if addr := w.RemoteAddr(); addr != nil {
		if udpAddr, ok := addr.(*net.UDPAddr); ok {
			clientIP = udpAddr.IP.String()
		} else if tcpAddr, ok := addr.(*net.TCPAddr); ok {
			clientIP = tcpAddr.IP.String()
		}
	}

	return clientIP
}

func (h *Handler) HandleRequest(w dns.ResponseWriter, req *dns.Msg) {
	// 提取客户端 IP
	clientIP := extractClientIPFromDNS(w, req)

	// 提取域名
	var domain string
	if len(req.Question) > 0 {
		domain = req.Question[0].Name
	}

	// 调用核心处理逻辑
	resp := h.HandleDnsMsg(req, clientIP, domain)

	// 写入响应
	if err := w.WriteMsg(resp); err != nil {
		h.logger.Printf("WriteMsg error: %+v", err)
	}
}

func uniqueAnswer(intSlice []dns.RR) []dns.RR {
	keys := make(map[string]bool)
	list := []dns.RR{}
	for _, entry := range intSlice {
		col := strings.Split(entry.String(), "\t")
		if _, value := keys[col[4]]; !value {
			keys[col[4]] = true
			list = append(list, entry)
		}
	}
	return list
}

func (h *Handler) getTheFullestResults(req *dns.Msg) []*dns.Msg {
	matchedUpstreams := h.matchedUpstreams(req)
	var wg sync.WaitGroup
	wg.Add(len(matchedUpstreams))
	msgs := make([]*dns.Msg, len(matchedUpstreams))

	for i := 0; i < len(matchedUpstreams); i++ {
		go func(j int) {
			defer wg.Done()
			msg, _, err := matchedUpstreams[j].Exchange(req.Copy())

			// 记录上游服务器统计
			if h.stats != nil {
				h.stats.RecordUpstreamQuery(matchedUpstreams[j].Address, err != nil)
			}

			if err != nil {
				h.logger.Printf("upstream error %s: %v %s", matchedUpstreams[j].Address, model.GetDomainNameFromDnsMsg(req), err)
				return
			}
			if matchedUpstreams[j].IsValidMsg(msg) {
				msgs[j] = msg
			}
		}(i)
	}

	wg.Wait()
	return msgs
}

func (h *Handler) getTheFastestResults(req *dns.Msg) []*dns.Msg {
	preferUpstreams := h.matchedUpstreams(req)
	msgs := make([]*dns.Msg, len(preferUpstreams))

	var mutex sync.Mutex
	var finishedCount int
	var finished bool
	var freedomIndex, primaryIndex []int

	var wg sync.WaitGroup
	wg.Add(1)

	for i := 0; i < len(preferUpstreams); i++ {
		go func(j int) {
			msg, _, err := preferUpstreams[j].Exchange(req.Copy())

			// 记录上游服务器统计
			if h.stats != nil {
				h.stats.RecordUpstreamQuery(preferUpstreams[j].Address, err != nil)
			}

			if err != nil {
				h.logger.Printf("upstream error %s: %v %s", preferUpstreams[j].Address, model.GetDomainNameFromDnsMsg(req), err)
			}

			mutex.Lock()
			defer mutex.Unlock()

			finishedCount++
			// 已经结束直接退出
			if finished {
				return
			}

			if err == nil {
				if preferUpstreams[j].IsValidMsg(msg) {
					if preferUpstreams[j].IsPrimary {
						primaryIndex = append(primaryIndex, j)
					} else {
						freedomIndex = append(freedomIndex, j)
					}
					msgs[j] = msg
				} else if preferUpstreams[j].IsPrimary {
					// 策略：国内 DNS 返回了 国外 服务器，计数但是不记入结果，以 国外 DNS 为准
					primaryIndex = append(primaryIndex, j)
				}
			}

			// 全部结束直接退出
			if finishedCount == len(preferUpstreams) {
				finished = true
				wg.Done()
				return
			}
			// 两组 DNS 都有一个返回结果，退出
			if len(primaryIndex) > 0 && len(freedomIndex) > 0 {
				finished = true
				wg.Done()
				return
			}
			// 满足任一条件退出
			//  - 国内 DNS 返回了 国内 服务器
			//  - 国内 DNS 返回国外服务器 且 国外 DNS 有可用结果
			if len(primaryIndex) > 0 && (msgs[primaryIndex[0]] != nil || len(freedomIndex) > 0) {
				finished = true
				wg.Done()
			}
		}(i)
	}

	wg.Wait()
	return msgs
}

func (h *Handler) getAnyResult(req *dns.Msg) []*dns.Msg {
	matchedUpstreams := h.matchedUpstreams(req)

	var wg sync.WaitGroup
	wg.Add(1)
	msgs := make([]*dns.Msg, len(matchedUpstreams))
	var mutex sync.Mutex
	var finishedCount int
	var finished bool

	for i := 0; i < len(matchedUpstreams); i++ {
		go func(j int) {
			msg, _, err := matchedUpstreams[j].Exchange(req.Copy())

			// 记录上游服务器统计
			if h.stats != nil {
				h.stats.RecordUpstreamQuery(matchedUpstreams[j].Address, err != nil)
			}

			if err != nil {
				h.logger.Printf("upstream error %s: %v %s", matchedUpstreams[j].Address, model.GetDomainNameFromDnsMsg(req), err)
			}
			mutex.Lock()
			defer mutex.Unlock()

			finishedCount++
			if finished {
				return
			}

			// 已结束或任意上游返回成功时退出
			if err == nil || finishedCount == len(matchedUpstreams) {
				finished = true
				msgs[j] = msg
				wg.Done()
			}
		}(i)
	}

	wg.Wait()
	return msgs
}

// Close properly shuts down the cache
func (h *Handler) Close() error {
	if h.builtInCache != nil {
		return h.builtInCache.Close()
	}
	return nil
}

// GetCacheStats returns cache statistics
func (h *Handler) GetCacheStats() string {
	if h.builtInCache != nil {
		return h.builtInCache.Stats()
	}
	return "Cache disabled"
}
