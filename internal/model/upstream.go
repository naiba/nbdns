package model

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/buraksezer/connpool"
	"github.com/miekg/dns"
	"github.com/pkg/errors"
	"github.com/yl2chen/cidranger"
	"go.uber.org/atomic"

	"github.com/naiba/nbdns/pkg/doh"
)

type Upstream struct {
	IsPrimary bool   `json:"is_primary,omitempty"`
	UseSocks  bool   `json:"use_socks,omitempty"`
	Address   string `json:"address,omitempty"`

	protocol, hostAndPort, host, port string
	config                            *Config
	ipRanger                          cidranger.Ranger

	pool      connpool.Pool
	dohClient *doh.Client
	bootstrap func(host string) (net.IP, error)

	count *atomic.Int64
}

func (up *Upstream) Init(config *Config, ipRanger cidranger.Ranger) {
	var ok bool
	up.protocol, up.hostAndPort, ok = strings.Cut(up.Address, "://")
	if ok && up.protocol != "https" {
		up.host, up.port, ok = strings.Cut(up.hostAndPort, ":")
	}
	if !ok {
		panic("上游地址格式(protocol://host:port)有误：" + up.Address)
	}

	if up.count != nil {
		panic("Upstream 已经初始化过了：" + up.Address)
	}

	up.count = atomic.NewInt64(0)
	up.config = config
	up.ipRanger = ipRanger
}

func (up *Upstream) Validate() error {
	if !up.IsPrimary && up.protocol == "udp" {
		return errors.New("非 primary 只能使用 tcp(-tls)/https：" + up.Address)
	}
	if up.IsPrimary && up.UseSocks {
		return errors.New("primary 无需接入 socks：" + up.Address)
	}
	if up.UseSocks && up.config.SocksProxy == "" {
		return errors.New("socks 未配置，但是上游已启用：" + up.Address)
	}
	if up.IsPrimary && up.protocol != "udp" {
		log.Println("[WARN] Primary 建议使用 udp 加速获取结果：" + up.Address)
	}
	return nil
}

func (up *Upstream) conntionFactory() (net.Conn, error) {
	if up.config.Debug {
		log.Printf("connecting to %s", up.Address)
	}

	addr := up.hostAndPort

	if up.bootstrap != nil {
		ip, err := up.bootstrap(up.host)
		if err != nil {
			addr = fmt.Sprintf("%s:%s", "0.0.0.0", up.port)
		} else {
			addr = fmt.Sprintf("%s:%s", ip.String(), up.port)
		}
	}

	if up.UseSocks {
		d, _, err := up.config.GetDialerContext(&net.Dialer{
			Timeout: time.Second * time.Duration(up.config.Timeout),
		})
		if err != nil {
			return nil, err
		}
		switch up.protocol {
		case "tcp":
			return d.Dial(up.protocol, addr)
		case "tcp-tls":
			conn, err := d.Dial("tcp", addr)
			if err != nil {
				return nil, err
			}
			return tls.Client(conn, &tls.Config{InsecureSkipVerify: true}), nil
		}
	} else {
		var d net.Dialer
		d.Timeout = time.Second * time.Duration(up.config.Timeout)
		switch up.protocol {
		case "tcp":
			return d.Dial(up.protocol, addr)
		case "tcp-tls":
			return tls.DialWithDialer(&d, "tcp", addr, nil)
		}
	}

	panic("wrong protocol: " + up.protocol)
}

func (up *Upstream) InitConnectionPool(bootstrap func(host string) (net.IP, error)) {
	up.bootstrap = bootstrap

	if strings.Contains(up.protocol, "http") {
		up.dohClient = doh.NewClient(doh.WithServer(up.Address),
			doh.WithDebug(up.config.Debug), doh.WithBootstrap(bootstrap),
			doh.WithSocksProxy(up.config.GetDialerContext),
		)
	}

	// 只需要启用 tcp/tcp-tls 协议的连接池
	if strings.Contains(up.protocol, "tcp") {
		p, err := connpool.NewChannelPool(0, 10, up.conntionFactory)
		if err != nil {
			log.Panicf("init upstream connection pool failed: %s", err)
		}
		up.pool = p
	}
}

func (up *Upstream) IsValidMsg(debug bool, r *dns.Msg) bool {
	if !up.IsPrimary {
		if debug {
			log.Printf("checkPrimary skip %s: %s %v %v", up.Address, r.Question[0].Name, r.Answer, up.IsPrimary)
		}
		return true
	}
	for i := 0; i < len(r.Answer); i++ {
		a, ok := r.Answer[i].(*dns.A)
		if !ok {
			continue
		}
		isPrimary, err := up.ipRanger.Contains(a.A)
		if err != nil {
			log.Printf("ipRanger query ip %s failed: %s", a.A, err)
			return true
		}
		if debug {
			log.Printf("checkPrimary result %s: %s@%s -> %v %v", up.Address, r.Question[0].Name, a.A, isPrimary, up.IsPrimary)
		}
		return isPrimary
	}
	return true
}

func (up *Upstream) poolLen() int {
	if up.pool == nil {
		return 0
	}
	return up.pool.Len()
}

func (up *Upstream) Exchange(req *dns.Msg) (*dns.Msg, time.Duration, error) {
	if up.config.Debug {
		log.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Inc(), up.poolLen(), runtime.NumGoroutine(), "enter")
		defer log.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Dec(), up.poolLen(), runtime.NumGoroutine(), "exit")
	}

	switch up.protocol {
	case "https":
		return up.dohClient.Exchange(req)
	case "udp":
		client := new(dns.Client)
		client.Timeout = time.Second * time.Duration(up.config.Timeout)
		return client.Exchange(req, up.hostAndPort)
	case "tcp", "tcp-tls":
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(up.config.Timeout))
		defer cancel()
		conn, err := up.pool.Get(ctx)
		if err != nil {
			return nil, 0, err
		}
		resp, err := dnsExchangeWithConn(conn, req, time.Second*time.Duration(up.config.Timeout))
		return resp, 0, err
	}
	panic(fmt.Sprintf("invalid upstream protocol: %s in address %s", up.protocol, up.Address))
}

func dnsExchangeWithConn(conn net.Conn, req *dns.Msg, timeout time.Duration) (*dns.Msg, error) {
	var resp *dns.Msg
	co := dns.Conn{Conn: conn}
	co.Conn.SetDeadline(time.Now().Add(timeout))
	err := co.WriteMsg(req)
	if err == nil {
		resp, err = co.ReadMsg()
	}
	if err != nil {
		c, yes := conn.(*connpool.PoolConn)
		if yes {
			c.MarkUnusable()
		}
	}
	conn.SetDeadline(time.Time{})
	co.Close()
	return resp, err
}
