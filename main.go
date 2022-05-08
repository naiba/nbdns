package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/naiba/nbdns/pkg/doh"
	"github.com/naiba/nbdns/pkg/qqwry"
)

type Upstream struct {
	IsPrimary bool   `json:"is_primary,omitempty"`
	Address   string `json:"address,omitempty"`
}

func (up *Upstream) IsValidMsg(r *dns.Msg) bool {
	for i := 0; i < len(r.Answer); i++ {
		col := strings.Split(r.Answer[i].String(), "\t")
		if len(col) < 5 || net.ParseIP(col[4]) == nil {
			continue
		}
		checkPrimary := up.checkPrimary(wry.Find(col[4]).Country)
		if (up.IsPrimary && !checkPrimary) || (!up.IsPrimary && checkPrimary) {
			return false
		}
	}
	return true
}

func (up *Upstream) checkPrimary(str string) bool {
	return strings.Contains(str, "省") || strings.Contains(str, "市") || strings.Contains(str, "自治区")
}

func (up *Upstream) Exchange(req *dns.Msg) (*dns.Msg, time.Duration, error) {
	protocol, addr, found := strings.Cut(up.Address, "://")
	if !found {
		log.Panicf("invalid upstream address: %s", up.Address)
	}

	switch protocol {
	case "https":
		c := doh.NewClient()
		return c.Exchange(req, up.Address)
	case "udp", "tcp", "tcp-tls":
		c := new(dns.Client)
		c.Net = protocol
		return c.Exchange(req, addr)
	}

	log.Panicf("invalid upstream protocol: %s in address %s", protocol, up.Address)
	return nil, 0, nil
}

const (
	_ = iota
	StrategyWaitForAll
	StrategyFastest
)

type Config struct {
	Upstreams []Upstream `json:"upstreams,omitempty"`
	Strategy  int        `json:"strategy,omitempty"`
}

func (c *Config) StrategyName() string {
	switch c.Strategy {
	case StrategyFastest:
		return "最快结果"
	case StrategyWaitForAll:
		return "最全结果"
	}
	panic("invalid strategy")
}

var wry *qqwry.QQwry
var config *Config

func init() {
	var err error
	wry, err = qqwry.NewQQwry("data/qqwry_lastest.dat")
	if err != nil {
		panic(err)
	}
	config = &Config{}
	body, err := ioutil.ReadFile("data/config.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(body), config); err != nil {
		panic(err)
	}
}

func main() {
	addr := "127.0.0.1:8853"
	server := &dns.Server{Addr: addr, Net: "udp"}
	dns.HandleFunc(".", handleRequest)
	log.Println("==== DNS Server ====")
	log.Println("端口:", addr)
	log.Println("模式:", config.StrategyName())
	server.ListenAndServe()
}

func handleRequest(w dns.ResponseWriter, req *dns.Msg) {
	var msgs []*dns.Msg

	switch config.Strategy {
	case StrategyWaitForAll:
		msgs = waitForAll(req)
	case StrategyFastest:
		msgs = getResultFastest(req)
	}

	var isPrimaryService *bool
	var res *dns.Msg

	for i := 0; i < len(msgs); i++ {
		if msgs[i] == nil {
			continue
		}

		if isPrimaryService == nil {
			isPrimaryService = &config.Upstreams[i].IsPrimary
		}
		if isPrimaryService == nil {
			continue
		}
		if *isPrimaryService == config.Upstreams[i].IsPrimary {
			if res == nil {
				res = msgs[i]
				continue
			}
			res.Answer = append(res.Answer, msgs[i].Answer...)
		}
	}

	if res == nil {
		return
	}
	res.Answer = unique(res.Answer)

	res.SetReply(req)
	w.WriteMsg(res)
}

func unique(intSlice []dns.RR) []dns.RR {
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

func waitForAll(req *dns.Msg) []*dns.Msg {
	var wg sync.WaitGroup
	wg.Add(len(config.Upstreams))
	msgs := make([]*dns.Msg, len(config.Upstreams))

	for i := 0; i < len(config.Upstreams); i++ {
		go func(j int) {
			defer func() {
				wg.Done()
			}()
			msg, _, err := config.Upstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", config.Upstreams[j].Address, req.Question, err)
				return
			}
			if config.Upstreams[j].IsValidMsg(msg) {
				msgs[j] = msg
			}
		}(i)
	}

	wg.Wait()
	return msgs
}

func getResultFastest(req *dns.Msg) []*dns.Msg {
	msgs := make([]*dns.Msg, len(config.Upstreams))

	var mutex sync.Mutex
	var finishedCount int
	var finished bool
	var freedomIndex, primaryIndex []int
	var wg sync.WaitGroup
	wg.Add(1)

	for i := 0; i < len(config.Upstreams); i++ {
		go func(j int) {
			defer func() {
				mutex.Lock()
				defer mutex.Unlock()
				finishedCount++
				// 已经结束直接退出
				if finished {
					return
				}
				// 全部结束直接退出
				if finishedCount == len(config.Upstreams) {
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
					return
				}
			}()

			msg, _, err := config.Upstreams[j].Exchange(req.Copy())
			if err != nil {
				log.Printf("upstream error %s: %v %s", config.Upstreams[j].Address, req.Question, err)
				return
			}

			mutex.Lock()
			if finished {
				return
			}
			if config.Upstreams[j].IsPrimary {
				if config.Upstreams[j].IsValidMsg(msg) {
					primaryIndex = append(primaryIndex, j)
					msgs[j] = msg
				} else {
					// 优化
					primaryIndex = append(primaryIndex, j)
				}
			} else if !config.Upstreams[j].IsPrimary && config.Upstreams[j].IsValidMsg(msg) {
				freedomIndex = append(freedomIndex, j)
				msgs[j] = msg
			}
			mutex.Unlock()
		}(i)
	}

	wg.Wait()
	return msgs
}
