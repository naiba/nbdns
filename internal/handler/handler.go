package handler

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/naiba/nbdns/internal/cache"
	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

type Handler struct {
	cacheNamespace                    string
	commonUpstreams, specialUpstreams []*model.Upstream
	builtInCache                      cache.Cache
	customRecords                     map[string][]dns.RR
	filter                            filter.Matcher
	logger                            logger.Logger
	stats                             stats.StatsRecorder
}

// SetFilter installs an immutable filter before the handler begins serving.
func (h *Handler) SetFilter(f filter.Matcher) { h.filter = f }

// SetCustomRecords validates and installs immutable overrides before serving requests.
func (h *Handler) SetCustomRecords(records []model.CustomRecord) error {
	compiled, err := model.CompileCustomRecords(records)
	if err != nil {
		return err
	}
	h.customRecords = compiled
	return nil
}

func NewHandler(builtInCache bool,
	upstreams []*model.Upstream,
	dataPath string,
	log logger.Logger,
	statsRecorder stats.StatsRecorder) *Handler {
	var c cache.Cache
	if builtInCache {
		var err error
		var diskCache *cache.BadgerCache
		diskCache, err = cache.NewBadgerCache(dataPath, log)
		if err != nil {
			log.Printf("Failed to initialize BadgerDB cache: %v", err)
			log.Printf("Cache will be disabled")
			c = nil
		} else {
			c = cache.NewMemoryCache(diskCache, 2048, 8<<20)
			log.Printf("BadgerDB cache and 8 MiB / 2048-entry hot cache initialized at %s", dataPath)
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
		cacheNamespace:   upstreamCacheNamespace(upstreams),
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
	if len(h.matchedUpstreams(req)) == 0 {
		return new(dns.Msg).SetRcode(req, dns.RcodeServerFailure)
	}

	// Each worker owns its request and sends once to a buffered channel. Late
	// replies cannot mutate the selected response or block after the caller returns.
	upstreams := h.matchedUpstreams(req)
	replies := make(chan *dns.Msg, len(upstreams))
	for _, upstream := range upstreams {
		query := req.Copy()
		go func(up *model.Upstream) {
			msg, _, err := up.Exchange(query)
			valid := err == nil && validUpstreamReply(query, msg) && !msg.Truncated &&
				(msg.Rcode == dns.RcodeSuccess || msg.Rcode == dns.RcodeNameError)
			if h.stats != nil {
				h.stats.RecordUpstreamQuery(up.Address, !valid)
			}
			if !valid {
				msg = nil
			}
			replies <- msg
		}(upstream)
	}
	for range upstreams {
		if msg := <-replies; msg != nil {
			return msg
		}
	}
	return new(dns.Msg).SetRcode(req, dns.RcodeServerFailure)
}

func upstreamCacheNamespace(upstreams []*model.Upstream) string {
	type policy struct {
		Address string
		Socks   bool
		Match   []string
		ECS     string
	}
	policies := make([]policy, 0, len(upstreams))
	for _, up := range upstreams {
		policies = append(policies, policy{up.Address, up.ProxyEnabled, up.Match, up.EffectiveECS()})
	}
	encoded, _ := json.Marshal(policies)
	// Cache namespaces isolate answers produced by different ECS and upstream policies.
	return fmt.Sprintf("ecs-v1:%x:", sha256.Sum256(encoded))
}

func getDnsRequestCacheKey(m *dns.Msg) string {
	var dnssec string
	if o := m.IsEdns0(); o != nil {
		// 区分 DNSSEC 请求，避免将非 DNSSEC 响应返回给需要 DNSSEC 的客户端
		if o.Do() {
			dnssec = "DO"
		}

	}
	return fmt.Sprintf("%s#%d#%d#%t#%s", strings.ToLower(model.GetDomainNameFromDnsMsg(m)), m.Question[0].Qtype, m.Question[0].Qclass, m.CheckingDisabled, dnssec)
}

func getDnsResponseTtl(m *dns.Msg) time.Duration {
	var ttl uint32
	if len(m.Answer) == 0 {
		// RFC 2308: negative answers are cacheable only with an SOA.
		for _, rr := range m.Ns {
			if soa, ok := rr.(*dns.SOA); ok {
				ttl = min(soa.Hdr.Ttl, soa.Minttl)
				break
			}
		}
	} else {
		ttl = m.Answer[0].Header().Ttl
		for _, rr := range m.Answer[1:] {
			ttl = min(ttl, rr.Header().Ttl)
		}
	}
	lifetime := time.Duration(min(ttl, 3600)) * time.Second
	for _, section := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range section {
			if sig, ok := rr.(*dns.RRSIG); ok {
				remaining := time.Until(time.Unix(int64(sig.Expiration), 0))
				if remaining < lifetime {
					lifetime = remaining
				}
			}
		}
	}
	return max(lifetime, 0)
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
	if req == nil {
		return new(dns.Msg).SetRcodeFormatError(new(dns.Msg))
	}
	h.logger.Printf("nbdns::request %+v\n", req)

	// 所有进入核心处理流程的请求都计入总查询数。
	if h.stats != nil {
		h.stats.RecordQuery()
	}
	// 提取域名（如果未提供）。域名榜单要等过滤结果确定后再分类记录。
	if domain == "" && len(req.Question) > 0 {
		domain = req.Question[0].Name
	}
	if req.Response || req.Opcode != dns.OpcodeQuery || len(req.Question) != 1 {
		return new(dns.Msg).SetRcodeFormatError(req)
	}
	// Local overrides take precedence over filtering and caches. A configured
	// owner is never forwarded, including queries for types absent locally.
	if records, exists := h.customRecords[strings.ToLower(dns.Fqdn(req.Question[0].Name))]; exists && req.Question[0].Qclass == dns.ClassINET {
		resp := new(dns.Msg).SetReply(req)
		resp.RecursionAvailable = true
		for _, rr := range records {
			if req.Question[0].Qtype == dns.TypeANY || rr.Header().Rrtype == req.Question[0].Qtype || rr.Header().Rrtype == dns.TypeCNAME {
				resp.Answer = append(resp.Answer, dns.Copy(rr))
			}
		}
		if h.stats != nil {
			h.stats.RecordClientQuery(clientIP, domain)
		}
		setResponseEDNS(req, resp)
		return resp
	}
	if h.filter != nil && h.filter.Blocked(req.Question[0].Name) {
		if h.stats != nil {
			h.stats.RecordBlockedQuery(clientIP, domain)
		}
		resp := new(dns.Msg).SetRcode(req, dns.RcodeNameError)
		resp.RecursionAvailable = true
		setResponseEDNS(req, resp)
		return resp
	}
	if h.stats != nil {
		h.stats.RecordClientQuery(clientIP, domain)
	}
	h.removeEDNS(req)

	// 检查缓存
	var cacheKey string
	var respCache *dns.Msg
	if h.builtInCache != nil {
		cacheKey = h.cacheNamespace + getDnsRequestCacheKey(req)
		if v, ok := h.builtInCache.Get(cacheKey); ok {
			if h.stats != nil {
				h.stats.RecordCacheHit()
			}
			respCache = v.Msg.Copy()
			if v.Expires.After(time.Now()) {
				msg := replyUpdateTtl(req, respCache, uint32(time.Until(v.Expires).Seconds()))
				setResponseEDNS(req, msg)
				return msg
			}
		} else {
			if h.stats != nil {
				h.stats.RecordCacheMiss()
			}
		}
	}

	// 从上游获取响应
	resp := h.exchange(req)

	if resp.Rcode == dns.RcodeServerFailure {
		if h.stats != nil {
			h.stats.RecordFailed()
		}
		// 上游失败时使用任何可用缓存（即使过期）作为降级
		if respCache != nil {
			msg := replyUpdateTtl(req, respCache, 12)
			if len(msg.Answer) > 0 && !req.CheckingDisabled && (req.IsEdns0() == nil || !req.IsEdns0().Do()) {
				msg.AuthenticatedData = false
				setResponseEDNS(req, msg)
				return msg
			}
		}
	}
	if len(resp.Answer) == 0 && (resp.Rcode == dns.RcodeNameError || resp.Rcode == dns.RcodeSuccess) {
		for _, rr := range resp.Ns {
			if soa, ok := rr.(*dns.SOA); ok {
				soa.Hdr.Ttl = min(soa.Hdr.Ttl, soa.Minttl)
			}
		}
	}

	restoreReplyMeta(req, resp)
	h.logger.Printf("nbdns::resp: %+v\n", resp)

	// 验证响应并缓存（防止缓存投毒）
	if h.builtInCache != nil && shouldCacheResponse(resp) && validateResponse(req, resp, h.logger) {
		if ttl := getDnsResponseTtl(resp); ttl > 0 {
			cachedMsg := &cache.CachedMsg{Msg: resp.Copy(), Expires: time.Now().Add(ttl)}
			if err := h.builtInCache.Set(cacheKey, cachedMsg, ttl+time.Hour); err != nil {
				h.logger.Printf("Failed to cache response: %v", err)
			}
		}
	}
	setResponseEDNS(req, resp)
	return resp
}

func restoreReplyMeta(req, resp *dns.Msg) {
	resp.Id = req.Id
	resp.Response = true
	resp.RecursionDesired = req.RecursionDesired
	resp.CheckingDisabled = req.CheckingDisabled
	resp.Question = []dns.Question{req.Question[0]}
}

func setResponseEDNS(req, resp *dns.Msg) {
	// OPT is transaction metadata (RFC 6891); do not reuse an upstream OPT.
	var extras []dns.RR
	for _, rr := range resp.Extra {
		if rr.Header().Rrtype != dns.TypeOPT {
			extras = append(extras, rr)
		}
	}
	resp.Extra = extras
	if opt := req.IsEdns0(); opt != nil {
		resp.SetEdns0(min(opt.UDPSize(), 1232), opt.Do())
	}
}

func validUpstreamReply(req, resp *dns.Msg) bool {
	return resp != nil && resp.Response && resp.Id == req.Id && resp.Opcode == req.Opcode &&
		len(resp.Question) == 1 && strings.EqualFold(resp.Question[0].Name, req.Question[0].Name) &&
		resp.Question[0].Qtype == req.Question[0].Qtype && resp.Question[0].Qclass == req.Question[0].Qclass
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
	if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
		truncateUDP(req, resp)
	}

	// 写入响应
	if err := w.WriteMsg(resp); err != nil {
		h.logger.Printf("WriteMsg error: %+v", err)
	}
}

func truncateUDP(req, resp *dns.Msg) {
	size := dns.MinMsgSize
	if opt := req.IsEdns0(); opt != nil {
		size = min(int(opt.UDPSize()), 1232)
	}
	resp.Truncate(size)
}

// uniqueAnswer 去除重复的 DNS 资源记录
// 基于域名、类型和记录数据进行去重，比字符串分割更高效和可靠
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

// replyUpdateTtl 准备缓存响应以发送给客户端，执行必要的修正：
// 1. 设置正确的 Message ID（通过 SetReply）
// 2. 更新所有 RR 的 TTL 为剩余时间（最低 0）
// 3. 调整 OPT RR 的 UDP size 为客户端请求的值
// 4. 清除 ECS Scope Length（标记为缓存答案）
// 5. 检查过期的 RRSIG 并移除
func replyUpdateTtl(req *dns.Msg, resp *dns.Msg, ttl uint32) *dns.Msg {
	now := time.Now().Unix()

	// 辅助函数：更新 RR 列表的 TTL，并检测过期 RRSIG
	updateRRs := func(rrs []dns.RR) []dns.RR {
		var validRRs []dns.RR
		for _, rr := range rrs {
			header := rr.Header()
			if header == nil {
				continue
			}

			// 检查 RRSIG 是否过期
			if rrsig, ok := rr.(*dns.RRSIG); ok {
				if rrsig.Expiration > 0 && uint32(now) > rrsig.Expiration {
					// RRSIG 已过期，跳过这条记录
					continue
				}
			}

			// 更新 TTL（最低为 0）
			header.Ttl = min(header.Ttl, ttl)
			validRRs = append(validRRs, rr)
		}
		return validRRs
	}

	// 更新所有部分的 TTL 并移除过期 RRSIG
	resp.Answer = updateRRs(resp.Answer)
	resp.Ns = updateRRs(resp.Ns)

	// Extra 部分需要特殊处理 OPT RR
	var validExtra []dns.RR
	var reqOpt *dns.OPT
	if reqOpt = req.IsEdns0(); reqOpt != nil {
		// 客户端有 EDNS0，获取其 UDP size
	}

	for _, rr := range resp.Extra {
		if opt, ok := rr.(*dns.OPT); ok {
			// 处理 OPT RR
			if reqOpt != nil {
				// 使用客户端请求的 UDP size
				opt.SetUDPSize(reqOpt.UDPSize())
			}

			// 清除 ECS Scope Length
			for i, option := range opt.Option {
				if ecs, ok := option.(*dns.EDNS0_SUBNET); ok {
					// 将 Scope Length 设为 0，表示这是缓存答案
					ecs.SourceScope = 0
					opt.Option[i] = ecs
				}
			}
			validExtra = append(validExtra, opt)
		} else {
			// 非 OPT RR，正常更新 TTL 和检查 RRSIG
			header := rr.Header()
			if header != nil {
				if rrsig, ok := rr.(*dns.RRSIG); ok {
					if rrsig.Expiration > 0 && uint32(now) > rrsig.Expiration {
						continue // 跳过过期的 RRSIG
					}
				}
				header.Ttl = min(header.Ttl, ttl)
			}
			validExtra = append(validExtra, rr)
		}
	}
	resp.Extra = validExtra

	// SetReply resets RCODE to NOERROR; preserve the cached response.
	restoreReplyMeta(req, resp)
	return resp
}
