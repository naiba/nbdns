package handler

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/singleton"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/cache"
)

type Handler struct {
	strategy                          int
	commonUpstreams, specialUpstreams []*model.Upstream
	builtInCache                      *cache.BadgerCache
}

func NewHandler(strategy int, builtInCache bool,
	upstreams []*model.Upstream,
	dataPath string) *Handler {
	var c *cache.BadgerCache
	if builtInCache {
		var err error
		c, err = cache.NewBadgerCache(dataPath)
		if err != nil {
			singleton.Logger.Printf("Failed to initialize BadgerDB cache: %v", err)
			c = nil
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
	return &Handler{strategy: strategy, commonUpstreams: commonUpstreams,
		specialUpstreams: specialUpstreams, builtInCache: c}
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
	res := h.Exchange(m)
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

	singleton.Logger.Printf("bootstrap LookupIP: %s %v --> %s %v", host, res.Answer, ip, err)
	return
}

func (h *Handler) Exchange(req *dns.Msg) *dns.Msg {
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
	var edns string
	o := m.IsEdns0()
	if o != nil {
		for _, s := range o.Option {
			switch e := s.(type) {
			case *dns.EDNS0_SUBNET:
				edns = e.Address.String()
			}
		}
	}
	return model.GetDomainNameFromDnsMsg(m) + "#" + strconv.Itoa(int(m.Question[0].Qtype)) + "#" + edns
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

func (h *Handler) HandleRequest(w dns.ResponseWriter, req *dns.Msg) {
	singleton.Logger.Printf("nbdns::request %+v\n", req)

	// 记录查询统计
	if stats.GlobalStats != nil {
		stats.GlobalStats.RecordQuery()
	}

	var m string
	if h.builtInCache != nil {
		m = getDnsRequestCacheKey(req)
		if v, ok := h.builtInCache.Get(m); ok {
			// 记录缓存命中
			if stats.GlobalStats != nil {
				stats.GlobalStats.RecordCacheHit()
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
			if err := w.WriteMsg(resp); err != nil {
				singleton.Logger.Printf("WriteMsg from cache error: %+v", err)
			}
			return
		}
		// 记录缓存未命中
		if stats.GlobalStats != nil {
			stats.GlobalStats.RecordCacheMiss()
		}
	}

	resp := h.Exchange(req)

	// 记录失败查询
	if resp.Rcode == dns.RcodeServerFailure && stats.GlobalStats != nil {
		stats.GlobalStats.RecordFailed()
	}

	resp.SetReply(req)
	if err := w.WriteMsg(resp); err != nil {
		singleton.Logger.Printf("WriteMsg from response error: %+v", err)
	}

	singleton.Logger.Printf("nbdns::resp: %+v\n", resp)

	if h.builtInCache != nil {
		ttl := getDnsResponseTtl(resp)
		cachedMsg := &cache.CachedMsg{
			Msg:     resp,
			Expires: time.Now().Add(ttl),
		}
		if err := h.builtInCache.Set(m, cachedMsg, ttl); err != nil {
			singleton.Logger.Printf("Failed to cache response: %v", err)
		}
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
			if stats.GlobalStats != nil {
				stats.GlobalStats.RecordUpstreamQuery(matchedUpstreams[j].Address, err != nil)
			}

			if err != nil {
				singleton.Logger.Printf("upstream error %s: %v %s", matchedUpstreams[j].Address, model.GetDomainNameFromDnsMsg(req), err)
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
			if stats.GlobalStats != nil {
				stats.GlobalStats.RecordUpstreamQuery(preferUpstreams[j].Address, err != nil)
			}

			if err != nil {
				singleton.Logger.Printf("upstream error %s: %v %s", preferUpstreams[j].Address, model.GetDomainNameFromDnsMsg(req), err)
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
			if stats.GlobalStats != nil {
				stats.GlobalStats.RecordUpstreamQuery(matchedUpstreams[j].Address, err != nil)
			}

			if err != nil {
				singleton.Logger.Printf("upstream error %s: %v %s", matchedUpstreams[j].Address, model.GetDomainNameFromDnsMsg(req), err)
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
