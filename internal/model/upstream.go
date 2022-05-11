package model

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/buraksezer/connpool"
	"github.com/miekg/dns"

	"github.com/naiba/nbdns/pkg/doh"
	"github.com/naiba/nbdns/pkg/qqwry"
)

const defaultTimeout = time.Second * 2

type Upstream struct {
	IsPrimary bool   `json:"is_primary,omitempty"`
	Address   string `json:"address,omitempty"`

	once           sync.Once
	pool           connpool.Pool
	protocol, host string
	debug          bool

	dohClient *doh.Client
	bootstrap func(host string) (net.IP, error)
}

func (up *Upstream) conntionFactory() (net.Conn, error) {
	if up.debug {
		log.Printf("connecting to %s", up.Address)
	}

	host := up.host

	if up.bootstrap != nil {
		_, addr, _ := strings.Cut(up.Address, "://")
		domain, port, _ := strings.Cut(addr, ":")

		ip, err := up.bootstrap(domain)
		if err != nil {
			domain = "127.0.0.1"
		} else {
			domain = ip.String()
		}

		host = fmt.Sprintf("%s:%s", domain, port)
	}

	var d net.Dialer
	d.Timeout = defaultTimeout
	switch up.protocol {
	case "tcp":
		return d.DialContext(context.Background(), up.protocol, host)
	case "tcp-tls":
		return tls.DialWithDialer(&d, "tcp", host, nil)
	}
	return nil, nil
}

func (up *Upstream) InitConnectionPool(debug bool, bootstrap func(host string) (net.IP, error)) {
	up.debug = debug
	up.bootstrap = bootstrap

	protocol, host, found := strings.Cut(up.Address, "://")
	if !found {
		log.Panicf("invalid upstream address: %s", up.Address)
	}
	up.protocol = protocol
	up.host = host

	if strings.Contains(up.protocol, "http") {
		up.dohClient = doh.NewClient(doh.WithServer(up.Address), doh.WithDebug(debug),
			doh.WithBootstrap(bootstrap))
	}

	// 只需要启用 tcp/tcp-tls 协议的连接池
	if strings.Contains(up.protocol, "tcp") {
		p, err := connpool.NewChannelPool(0, 10, up.conntionFactory)
		if err != nil {
			log.Panicf("init upstream connection pool failed: %s", err)
		}
		up.pool = p
		return
	}
}

func (up *Upstream) IsValidMsg(ipdb *qqwry.QQwry, debug bool, r *dns.Msg) bool {
	for i := 0; i < len(r.Answer); i++ {
		col := strings.Split(r.Answer[i].String(), "\t")
		if len(col) < 5 || net.ParseIP(col[4]) == nil {
			continue
		}
		country := ipdb.Find(col[4]).Country
		checkPrimary := up.checkPrimary(country)
		if debug {
			log.Printf("%s: %s@%s -> %s %v %v", up.Address, r.Question[0].Name, col[4], country, checkPrimary, up.IsPrimary)
		}
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
	switch up.protocol {
	case "https":
		return up.dohClient.Exchange(req)
	case "udp":
		resp, err := dns.Exchange(req, up.host)
		return resp, 0, err
	case "tcp", "tcp-tls":
		conn, err := up.pool.Get(context.Background())
		if err != nil {
			return nil, 0, err
		}
		var resp *dns.Msg
		co := dns.Conn{Conn: conn}
		err = co.WriteMsg(req)
		if err == nil {
			resp, err = co.ReadMsg()
		}
		if err != nil {
			c := conn.(*connpool.PoolConn)
			c.MarkUnusable()
		}
		co.Close()
		return resp, 0, err
	}
	log.Panicf("invalid upstream protocol: %s in address %s", up.protocol, up.Address)
	return nil, 0, nil
}
