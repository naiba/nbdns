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

type Config struct {
	Upstreams []Upstream
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
	log.Println("DNS Server 已启动:", addr)
	server.ListenAndServe()
}

func handleRequest(w dns.ResponseWriter, req *dns.Msg) {
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
			msgs[j] = msg
		}(i)
	}
	wg.Wait()

	var res *dns.Msg
	var isPrimaryService *bool

	for i := 0; i < len(msgs); i++ {
		if msgs[i] == nil {
			continue
		}

		if isPrimaryService == nil && config.Upstreams[i].IsValidMsg(msgs[i]) {
			isPrimaryService = &config.Upstreams[i].IsPrimary
		}
		if isPrimaryService == nil {
			continue
		}
		if *isPrimaryService == config.Upstreams[i].IsPrimary && config.Upstreams[i].IsValidMsg(msgs[i]) {
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
