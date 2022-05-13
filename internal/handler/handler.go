package handler

import (
	"errors"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
)

type Handler struct {
	strategy  int
	upstreams []model.Upstream
	debug     bool
}

func NewHandler(strategy int,
	upstreams []model.Upstream,
	debug bool) *Handler {
	return &Handler{strategy: strategy, upstreams: upstreams, debug: debug}
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

	var isPrimaryService *bool
	var res *dns.Msg

	for i := 0; i < len(msgs); i++ {
		if msgs[i] == nil {
			continue
		}

		if isPrimaryService == nil {
			isPrimaryService = &h.upstreams[i].IsPrimary
		}
		if isPrimaryService == nil {
			continue
		}

		if *isPrimaryService == h.upstreams[i].IsPrimary {
			if res == nil {
				res = msgs[i]
				continue
			}
			res.Answer = append(res.Answer, msgs[i].Answer...)
		}
	}

	if res == nil {
		return new(dns.Msg)
	}

	res.Answer = uniqueAnswer(res.Answer)
	return res
}

func (h *Handler) HandleRequest(w dns.ResponseWriter, req *dns.Msg) {
	res := h.Exchange(req)
	res.SetReply(req)
	w.WriteMsg(res)
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
	var wg sync.WaitGroup
	wg.Add(len(h.upstreams))
	msgs := make([]*dns.Msg, len(h.upstreams))

	for i := 0; i < len(h.upstreams); i++ {
		go func(j int) {
			defer wg.Done()
			msg, _, err := h.upstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", h.upstreams[j].Address, req.Question[0].Name, err)
				return
			}
			if h.upstreams[j].IsValidMsg(h.debug, msg) {
				msgs[j] = msg
			}
		}(i)
	}

	wg.Wait()
	return msgs
}

func (h *Handler) getTheFastestResults(req *dns.Msg) []*dns.Msg {
	msgs := make([]*dns.Msg, len(h.upstreams))

	var mutex sync.Mutex
	var finishedCount int
	var finished bool
	var freedomIndex, primaryIndex []int

	var wg sync.WaitGroup
	wg.Add(1)

	for i := 0; i < len(h.upstreams); i++ {
		go func(j int) {
			msg, _, err := h.upstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", h.upstreams[j].Address, req.Question[0].Name, err)
				return
			}

			mutex.Lock()
			defer mutex.Unlock()

			finishedCount++
			// 已经结束直接退出
			if finished {
				return
			}

			if h.upstreams[j].IsValidMsg(h.debug, msg) {
				if h.upstreams[j].IsPrimary {
					primaryIndex = append(primaryIndex, j)
				} else {
					freedomIndex = append(freedomIndex, j)
				}
				msgs[j] = msg
			} else if h.upstreams[j].IsPrimary {
				// 优化
				primaryIndex = append(primaryIndex, j)
			}

			// 全部结束直接退出
			if finishedCount == len(h.upstreams) {
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
	var wg sync.WaitGroup
	wg.Add(1)
	msgs := make([]*dns.Msg, len(h.upstreams))
	var mutex sync.Mutex
	var finishedCount int
	var finished bool

	for i := 0; i < len(h.upstreams); i++ {
		go func(j int) {
			msg, _, err := h.upstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", h.upstreams[j].Address, req.Question[0].Name, err)
			}
			mutex.Lock()
			defer mutex.Unlock()
			if finished {
				return
			}
			finishedCount++
			if err == nil || finishedCount == len(h.upstreams) {
				finished = true
				msgs[j] = msg
				wg.Done()
			}
		}(i)
	}

	wg.Wait()
	return msgs
}
