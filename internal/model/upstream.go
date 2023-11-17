package model

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/dropbox/godropbox/net2"
	"github.com/miekg/dns"
	"github.com/pkg/errors"
	"github.com/yl2chen/cidranger"
	"go.uber.org/atomic"

	"github.com/naiba/nbdns/pkg/doh"
)

type Upstream struct {
	IsPrimary bool     `json:"is_primary,omitempty"`
	UseSocks  bool     `json:"use_socks,omitempty"`
	Address   string   `json:"address,omitempty"`
	Match     []string `json:"match,omitempty"`

	protocol, hostAndPort, host, port string
	config                            *Config
	ipRanger                          cidranger.Ranger
	matchRegexp                       []*regexp.Regexp

	pool      net2.ConnectionPool
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

	if up.Match != nil {
		for _, m := range up.Match {
			up.matchRegexp = append(up.matchRegexp, regexp.MustCompile(m))
		}
	}

	up.count = atomic.NewInt64(0)
	up.config = config
	up.ipRanger = ipRanger
}

func (up *Upstream) IsMatch(domain string) bool {
	for _, reg := range up.matchRegexp {
		if reg.MatchString(domain) {
			return true
		}
	}
	return false
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

func (up *Upstream) conntionFactory(network, address string) (net.Conn, error) {
	if up.config.Debug {
		log.Printf("connecting to %s://%s", network, address)
	}

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	if up.bootstrap != nil && net.ParseIP(host) == nil {
		ip, err := up.bootstrap(host)
		if err != nil {
			address = fmt.Sprintf("%s:%s", "0.0.0.0", port)
		} else {
			address = fmt.Sprintf("%s:%s", ip.String(), port)
		}
	}

	if up.UseSocks {
		d, _, err := up.config.GetDialerContext(&net.Dialer{
			Timeout: time.Second * time.Duration(up.config.Timeout),
		})
		if err != nil {
			return nil, err
		}
		switch network {
		case "tcp":
			return d.Dial(network, address)
		case "tcp-tls":
			conn, err := d.Dial("tcp", address)
			if err != nil {
				return nil, err
			}
			return tls.Client(conn, &tls.Config{
				ServerName: host,
			}), nil
		}
	} else {
		var d net.Dialer
		d.Timeout = time.Second * time.Duration(up.config.Timeout)
		switch network {
		case "tcp":
			return d.Dial(network, address)
		case "tcp-tls":
			return tls.DialWithDialer(&d, "tcp", address, &tls.Config{
				ServerName: host,
			})
		}
	}

	panic("wrong protocol: " + network)
}

func (up *Upstream) InitConnectionPool(bootstrap func(host string) (net.IP, error)) {
	up.bootstrap = bootstrap

	if strings.Contains(up.protocol, "http") {
		ops := []doh.ClientOption{
			doh.WithServer(up.Address),
			doh.WithDebug(up.config.Debug),
			doh.WithBootstrap(bootstrap),
			doh.WithTimeout(time.Second * time.Duration(up.config.Timeout)),
		}
		if up.UseSocks {
			ops = append(ops, doh.WithSocksProxy(up.config.GetDialerContext))
		}
		up.dohClient = doh.NewClient(ops...)
	}

	// 只需要启用 tcp/tcp-tls 协议的连接池
	if strings.Contains(up.protocol, "tcp") {
		maxIdleTime := time.Second * time.Duration(up.config.Timeout*10)
		timeout := time.Second * time.Duration(up.config.Timeout)
		p := net2.NewSimpleConnectionPool(net2.ConnectionOptions{
			MaxActiveConnections: 10,
			MaxIdleConnections:   5,
			MaxIdleTime:          &maxIdleTime,
			DialMaxConcurrency:   10,
			ReadTimeout:          timeout,
			WriteTimeout:         timeout,
			Dial: func(network, address string) (net.Conn, error) {
				dialer, err := up.conntionFactory(network, address)
				if err != nil {
					return nil, err
				}
				dialer.SetDeadline(time.Now().Add(timeout))
				return dialer, nil
			},
		})
		p.Register(up.protocol, up.hostAndPort)
		up.pool = p
	}
}

func (up *Upstream) IsValidMsg(debug bool, r *dns.Msg) bool {
	var inBlacklist bool
	domain := GetDomainNameFronDnsMsg(r)
	if domain != "" {
		for _, reg := range up.config.BlacklistRegexp {
			if reg.MatchString(domain) {
				inBlacklist = true
				break
			}
		}
	}

	var hasValidAnswer bool

	for i := 0; i < len(r.Answer); i++ {
		a, ok := r.Answer[i].(*dns.A)
		if !ok {
			continue
		}
		isPrimary, err := up.ipRanger.Contains(a.A)
		if err != nil {
			log.Printf("ipRanger query ip %s failed: %s", a.A, err)
			continue
		}
		if debug {
			log.Printf("checkPrimary result %s: %s@%s ->domain.inBlacklist:%v ip.IsPrimary:%v up.IsPrimary:%v", up.Address, GetDomainNameFronDnsMsg(r), a.A, inBlacklist, isPrimary, up.IsPrimary)
		}

		// 黑名单中的域名，如果是 primary 即不可用
		if inBlacklist && isPrimary {
			return false
		}
		// 如果是 server 是 primary，但是 ip 不是 primary，也不可用
		if up.IsPrimary && !isPrimary {
			return false
		}

		hasValidAnswer = true
	}

	return hasValidAnswer
}

func GetDomainNameFronDnsMsg(msg *dns.Msg) string {
	if msg == nil || len(msg.Question) == 0 {
		return ""
	}
	return msg.Question[0].Name
}

func (up *Upstream) poolLen() int32 {
	if up.pool == nil {
		return 0
	}
	return up.pool.NumActive()
}

func (up *Upstream) Exchange(req *dns.Msg) (*dns.Msg, time.Duration, error) {
	if up.config.Debug {
		log.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Inc(), up.poolLen(), runtime.NumGoroutine(), "enter")
		defer log.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Dec(), up.poolLen(), runtime.NumGoroutine(), "exit")
	}

	var resp *dns.Msg
	var duration time.Duration
	var err error

	switch up.protocol {
	case "https", "http":
		resp, duration, err = up.dohClient.Exchange(req)
	case "udp":
		client := new(dns.Client)
		client.Timeout = time.Second * time.Duration(up.config.Timeout)
		resp, duration, err = client.Exchange(req, up.hostAndPort)
	case "tcp", "tcp-tls":
		conn, errGetConn := up.pool.Get(up.protocol, up.hostAndPort)
		if errGetConn != nil {
			return nil, 0, errGetConn
		}
		resp, err = dnsExchangeWithConn(conn, req)
	default:
		panic(fmt.Sprintf("invalid upstream protocol: %s in address %s", up.protocol, up.Address))
	}

	// 清理 EDNS 信息
	if resp != nil && len(resp.Extra) > 0 {
		var newExtra []dns.RR
		for i := 0; i < len(resp.Extra); i++ {
			if resp.Extra[i].Header().Rrtype == dns.TypeOPT {
				continue
			}
			newExtra = append(newExtra, resp.Extra[i])
		}
		resp.Extra = newExtra
	}

	return resp, duration, err
}

func dnsExchangeWithConn(conn net2.ManagedConn, req *dns.Msg) (*dns.Msg, error) {
	var resp *dns.Msg
	co := dns.Conn{Conn: conn}
	err := co.WriteMsg(req)
	if err == nil {
		resp, err = co.ReadMsg()
	}
	if err == nil {
		conn.ReleaseConnection()
	} else {
		conn.DiscardConnection()
	}
	return resp, err
}
