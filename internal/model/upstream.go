package model

import (
	"crypto/tls"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/dropbox/godropbox/net2"
	"github.com/miekg/dns"
	"github.com/pkg/errors"
	"github.com/yl2chen/cidranger"
	"go.uber.org/atomic"

	"github.com/naiba/nbdns/pkg/doh"
	"github.com/naiba/nbdns/pkg/logger"
	"github.com/naiba/nbdns/pkg/utils"
)

type Upstream struct {
	IsPrimary bool     `json:"is_primary,omitempty"`
	UseSocks  bool     `json:"use_socks,omitempty"`
	Address   string   `json:"address,omitempty"`
	Match     []string `json:"match,omitempty"`

	protocol, hostAndPort, host, port string
	config                            *Config
	ipRanger                          cidranger.Ranger
	matchSplited                      [][]string

	pool      net2.ConnectionPool
	dohClient *doh.Client
	bootstrap func(host string) (net.IP, error)
	logger    logger.Logger

	count *atomic.Int64
}

func (up *Upstream) Init(config *Config, ipRanger cidranger.Ranger, log logger.Logger) {
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

	up.matchSplited = utils.ParseRules(up.Match)
	up.count = atomic.NewInt64(0)
	up.config = config
	up.ipRanger = ipRanger
	up.logger = log
}

func (up *Upstream) IsMatch(domain string) bool {
	return utils.HasMatchedRule(up.matchSplited, domain)
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
		up.logger.Println("[WARN] Primary 建议使用 udp 加速获取结果：" + up.Address)
	}
	return nil
}

func (up *Upstream) conntionFactory(network, address string) (net.Conn, error) {
	up.logger.Printf("connecting to %s://%s", network, address)

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
			doh.WithBootstrap(bootstrap),
			doh.WithTimeout(time.Second * time.Duration(up.config.Timeout)),
			doh.WithLogger(up.logger),
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

func (up *Upstream) IsValidMsg(r *dns.Msg) bool {
	domain := GetDomainNameFromDnsMsg(r)
	inBlacklist := utils.HasMatchedRule(up.config.BlacklistSplited, domain)
	for i := 0; i < len(r.Answer); i++ {
		var ip net.IP
		typeA, ok := r.Answer[i].(*dns.A)
		if ok {
			ip = typeA.A
		} else {
			typeAAAA, ok := r.Answer[i].(*dns.AAAA)
			if !ok {
				continue
			}
			ip = typeAAAA.AAAA
		}
		isPrimary, err := up.ipRanger.Contains(ip)
		if err != nil {
			up.logger.Printf("ipRanger query ip %s failed: %s", ip, err)
			continue
		}

		up.logger.Printf("checkPrimary result %s: %s@%s ->domain.inBlacklist:%v ip.IsPrimary:%v up.IsPrimary:%v", up.Address, domain, ip, inBlacklist, isPrimary, up.IsPrimary)

		// 黑名单中的域名，如果是 primary 即不可用
		if inBlacklist && isPrimary {
			return false
		}
		// 如果是 server 是 primary，但是 ip 不是 primary，也不可用
		if up.IsPrimary && !isPrimary {
			return false
		}
	}
	return !up.IsPrimary || len(r.Answer) > 0
}

func GetDomainNameFromDnsMsg(msg *dns.Msg) string {
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
	up.logger.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Inc(), up.poolLen(), runtime.NumGoroutine(), "enter")
	defer up.logger.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Dec(), up.poolLen(), runtime.NumGoroutine(), "exit")

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
