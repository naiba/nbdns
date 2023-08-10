package handler

import (
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/patrickmn/go-cache"
)

type Handler struct {
	strategy              int
	upstreams             []*model.Upstream
	preferDomainUpstreams map[string][]*model.Upstream
	builtInCache          *cache.Cache
	debug                 bool
}

func NewHandler(strategy int, builtInCache bool,
	upstreams []*model.Upstream,
	debug bool) *Handler {
	var c *cache.Cache
	if builtInCache {
		c = cache.New(time.Minute, time.Minute*10)
	}
	var preferDomainUpstreams = make(map[string][]*model.Upstream)
	for _, u := range upstreams {
		for _, d := range u.PreferDomain {
			preferDomainUpstreams[d] = append(preferDomainUpstreams[d], u)
		}
	}
	return &Handler{strategy: strategy, upstreams: upstreams, debug: debug, builtInCache: c, preferDomainUpstreams: preferDomainUpstreams}
}

func (h *Handler) preferUpstreams(req *dns.Msg) []*model.Upstream {
	if len(req.Question) == 0 {
		return h.upstreams
	}
	q := req.Question[0]
	if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
		return h.upstreams
	}
	suffixs := strings.Split(q.Name, ".")
	if len(suffixs) < 2 {
		return h.upstreams
	}
	preferUpstreams := h.preferDomainUpstreams[suffixs[len(suffixs)-2]]
	if len(preferUpstreams) > 0 {
		return preferUpstreams
	}
	return h.upstreams
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
	if h.debug {
		log.Printf("bootstrap LookupIP: %s %v --> %s %v", host, res.Answer, ip, err)
	}
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

type CachedMsg struct {
	msg     *dns.Msg
	expires time.Time
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
	return m.Question[0].Name + "#" + strconv.Itoa(int(m.Question[0].Qtype)) + "#" + edns
}

func getDnsResponseTtl(m *dns.Msg) time.Duration {
	var ttl uint32
	if len(m.Answer) == 0 {
		ttl = 60 // 最小 ttl 1 分钟
	} else {
		ttl = m.Answer[0].Header().Ttl
	}
	if ttl > 3600 {
		ttl = 3600 // 最大 ttl 1 小时
	}
	return time.Duration(ttl) * time.Second
}

func (h *Handler) HandleRequest(w dns.ResponseWriter, req *dns.Msg) {
	if h.debug {
		log.Printf("HandleRequest req: %+v\n", req)
	}

	var m string
	if h.builtInCache != nil {
		m = getDnsRequestCacheKey(req)
		if v, ok := h.builtInCache.Get(m); ok {
			v := v.(*CachedMsg)
			resp := v.msg.Copy()
			// 更新缓存的 answer 的 TTL
			for i := 0; i < len(resp.Answer); i++ {
				header := resp.Answer[i].Header()
				if header == nil {
					continue
				}
				header.Ttl = uint32(time.Until(v.expires).Seconds())
			}
			resp.SetReply(req)
			if err := w.WriteMsg(resp); err != nil {
				log.Printf("WriteMsg from cache error: %+v", err)
			}
			return
		}
	}

	resp := h.Exchange(req)
	resp.SetReply(req)
	if err := w.WriteMsg(resp); err != nil {
		log.Printf("WriteMsg from response error: %+v", err)
	}

	if h.debug {
		log.Printf("HandleRequest resp: %+v\n", resp)
	}

	if h.builtInCache != nil {
		h.builtInCache.Set(m, &CachedMsg{
			msg:     resp,
			expires: time.Now().Add(getDnsResponseTtl(resp)),
		}, getDnsResponseTtl(resp))
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
	preferUpstreams := h.preferUpstreams(req)
	var wg sync.WaitGroup
	wg.Add(len(preferUpstreams))
	msgs := make([]*dns.Msg, len(preferUpstreams))

	for i := 0; i < len(preferUpstreams); i++ {
		go func(j int) {
			defer wg.Done()
			msg, _, err := preferUpstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", preferUpstreams[j].Address, req.Question[0].Name, err)
				return
			}
			if preferUpstreams[j].IsValidMsg(h.debug, msg) {
				msgs[j] = msg
			}
		}(i)
	}

	wg.Wait()
	return msgs
}

func (h *Handler) getTheFastestResults(req *dns.Msg) []*dns.Msg {
	preferUpstreams := h.preferUpstreams(req)
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
			if err != nil {
				log.Printf("upstream error %s: %v %s", preferUpstreams[j].Address, req.Question[0].Name, err)
			}

			mutex.Lock()
			defer mutex.Unlock()

			finishedCount++
			// 已经结束直接退出
			if finished {
				return
			}

			if err == nil {
				if preferUpstreams[j].IsValidMsg(h.debug, msg) {
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
	preferUpstreams := h.preferUpstreams(req)

	var wg sync.WaitGroup
	wg.Add(1)
	msgs := make([]*dns.Msg, len(preferUpstreams))
	var mutex sync.Mutex
	var finishedCount int
	var finished bool

	for i := 0; i < len(preferUpstreams); i++ {
		go func(j int) {
			msg, _, err := preferUpstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", preferUpstreams[j].Address, req.Question[0].Name, err)
			}
			mutex.Lock()
			defer mutex.Unlock()

			finishedCount++
			if finished {
				return
			}

			// 已结束或任意上游返回成功时退出
			if err == nil || finishedCount == len(preferUpstreams) {
				finished = true
				msgs[j] = msg
				wg.Done()
			}
		}(i)
	}

	wg.Wait()
	return msgs
}
