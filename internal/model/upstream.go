package model

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/buraksezer/connpool"
	"github.com/miekg/dns"
	"github.com/pkg/errors"
	"go.uber.org/atomic"

	"github.com/naiba/nbdns/pkg/doh"
	"github.com/naiba/nbdns/pkg/qqwry"
)

const defaultTimeout = time.Second * 2

type Upstream struct {
	IsPrimary bool   `json:"is_primary,omitempty"`
	Address   string `json:"address,omitempty"`

	once                      sync.Once
	pool                      connpool.Pool
	protocol, addr, hos, port string
	debug                     bool

	dohClient *doh.Client
	bootstrap func(host string) (net.IP, error)

	count *atomic.Int64
}

func (up *Upstream) Init(debug bool) {
	var ok bool
	up.protocol, up.addr, ok = strings.Cut(up.Address, "://")
	if ok && up.protocol != "https" {
		up.hos, up.port, ok = strings.Cut(up.addr, ":")
	}
	if !ok {
		panic("上游地址格式(protocol://host:port)有误：" + up.Address)
	}

	if up.count != nil {
		panic("Upstream 已经初始化过了：" + up.Address)
	}

	up.count = atomic.NewInt64(0)
	up.debug = debug
}

func (up *Upstream) Validate() error {
	if !up.IsPrimary && up.protocol == "udp" {
		return errors.New("非 primary 只能使用 tcp(-tls)/https：" + up.Address)
	}
	if up.IsPrimary && up.protocol != "udp" {
		log.Println("[WARN] Primary 建议使用 udp 加速获取结果：" + up.Address)
	}
	return nil
}

func (up *Upstream) conntionFactory() (net.Conn, error) {
	if up.debug {
		log.Printf("connecting to %s", up.Address)
	}

	addr := up.addr

	if up.bootstrap != nil {
		ip, err := up.bootstrap(up.hos)
		if err != nil {
			addr = fmt.Sprintf("%s:%s", "0.0.0.0", up.port)
		} else {
			addr = fmt.Sprintf("%s:%s", ip.String(), up.port)
		}
	}

	var d net.Dialer
	d.Timeout = defaultTimeout
	switch up.protocol {
	case "tcp":
		return d.Dial(up.protocol, addr)
	case "tcp-tls":
		return tls.DialWithDialer(&d, "tcp", addr, nil)
	}
	return nil, nil
}

func (up *Upstream) InitConnectionPool(bootstrap func(host string) (net.IP, error)) {
	up.bootstrap = bootstrap

	if strings.Contains(up.protocol, "http") {
		up.dohClient = doh.NewClient(doh.WithServer(up.Address), doh.WithDebug(up.debug),
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

func (up *Upstream) IsValidMsg(debug bool, r *dns.Msg) bool {
	for i := 0; i < len(r.Answer); i++ {
		col := strings.Split(r.Answer[i].String(), "\t")
		if len(col) < 5 || net.ParseIP(col[4]) == nil {
			continue
		}
		country, _, err := qqwry.QueryIP(col[4])
		if err != nil {
			log.Printf("qqwry query ip %s failed: %s", col[4], err)
			return true
		}
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

func (up *Upstream) poolLen() int {
	if up.pool == nil {
		return 0
	}
	return up.pool.Len()
}

func (up *Upstream) Exchange(req *dns.Msg) (*dns.Msg, time.Duration, error) {
	if up.debug {
		log.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Inc(), up.poolLen(), runtime.NumGoroutine(), "enter")
		defer log.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Dec(), up.poolLen(), runtime.NumGoroutine(), "exit")
	}

	switch up.protocol {
	case "https":
		return up.dohClient.Exchange(req)
	case "udp":
		client := new(dns.Client)
		client.Timeout = defaultTimeout
		return client.Exchange(req, up.addr)
	case "tcp", "tcp-tls":
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		conn, err := up.pool.Get(ctx)
		if err != nil {
			return nil, 0, err
		}
		var resp *dns.Msg
		co := dns.Conn{Conn: conn}
		co.Conn.SetDeadline(time.Now().Add(defaultTimeout))
		err = co.WriteMsg(req)
		if err == nil {
			resp, err = co.ReadMsg()
		}
		if err != nil {
			c := conn.(*connpool.PoolConn)
			c.MarkUnusable()
		}
		conn.SetDeadline(time.Time{})
		co.Close()
		return resp, 0, err
	}
	log.Panicf("invalid upstream protocol: %s in address %s", up.protocol, up.Address)
	return nil, 0, nil
}
