package model

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dropbox/godropbox/net2"
	"github.com/miekg/dns"
	"github.com/pkg/errors"
	"go.uber.org/atomic"
	"net/netip"

	"github.com/naiba/nbdns/pkg/doh"
	"github.com/naiba/nbdns/pkg/logger"
	"github.com/naiba/nbdns/pkg/utils"
)

type Upstream struct {
	ECS          *string  `json:"ecs,omitempty"`
	ProxyEnabled bool     `json:"proxy_enabled,omitempty"`
	Address      string   `json:"address,omitempty"`
	Match        []string `json:"match,omitempty"`
	MatchLists   []string `json:"match_lists,omitempty"` // Domain list subscriptions or files, like filter_lists

	protocol, hostAndPort, host, port string
	config                            *Config
	ecsPrefix                         netip.Prefix
	matchSplited                      [][]string
	matchSets                         atomic.Pointer[[]DomainSet] // Loaded match_lists

	pool      net2.ConnectionPool
	dohClient *doh.Client
	bootstrap func(host string) (net.IP, error)
	logger    logger.Logger

	count *atomic.Int64
}

func (up *Upstream) Init(config *Config, log logger.Logger) {
	var ok bool
	up.protocol, up.hostAndPort, ok = strings.Cut(up.Address, "://")
	if ok && up.protocol != "https" {
		var err error
		up.host, up.port, err = net.SplitHostPort(up.hostAndPort)
		ok = err == nil
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
	up.ecsPrefix, _ = parseECS(up.EffectiveECS())
	up.logger = log
}

// SetLogger 更新 upstream 的 logger 实例
func (up *Upstream) SetLogger(log logger.Logger) {
	up.logger = log
}

// DomainSet is a loaded domain list (see filter.NewDomainListManager).
type DomainSet interface {
	Contains(name string) bool
}

// SetMatchLists installs the loaded match_lists; their contents refresh in place.
func (up *Upstream) SetMatchLists(sets ...DomainSet) {
	up.matchSets.Store(&sets)
}

// HasMatchRules reports whether this is a dedicated upstream: it only answers
// names matching its rules or lists, never acting as a default upstream.
func (up *Upstream) HasMatchRules() bool {
	return len(up.Match) > 0 || len(up.MatchLists) > 0
}

func (up *Upstream) IsMatch(domain string) bool {
	if utils.HasMatchedRule(up.matchSplited, domain) {
		return true
	}
	// Lists that have not loaded yet (first start without cache) match nothing,
	// so their names keep using the default upstreams meanwhile.
	if sets := up.matchSets.Load(); sets != nil {
		for _, set := range *sets {
			if set.Contains(domain) {
				return true
			}
		}
	}
	return false
}

func (up *Upstream) Validate() error {
	if _, err := parseECS(up.EffectiveECS()); err != nil {
		return err
	}
	for _, entry := range up.MatchLists {
		if strings.TrimSpace(entry) == "" {
			return errors.New("empty match_lists entry: " + up.Address)
		}
	}
	if up.ProxyEnabled && up.protocol == "udp" {
		return errors.New("SOCKS upstreams require TCP, TLS or HTTPS")
	}
	if up.ProxyEnabled && up.config.SocksProxy == "" {
		return errors.New("socks 未配置，但是上游已启用：" + up.Address)
	}
	return nil
}

func (up *Upstream) conntionFactory(network, address string) (net.Conn, error) {
	return up.dialUpstreamContext(context.Background(), network, address)
}

func (up *Upstream) dialUpstreamContext(ctx context.Context, network, address string) (net.Conn, error) {
	if up.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(up.config.Timeout)*time.Second)
		defer cancel()
	}
	up.logger.Printf("connecting to %s://%s", network, address)

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	if up.bootstrap != nil && net.ParseIP(host) == nil {
		ip, err := up.bootstrap(host)
		if err != nil {
			// A failed lookup must not turn into a connection to 0.0.0.0,
			// which can reach a local service instead of the intended upstream.
			return nil, errors.Wrap(err, "bootstrap")
		}
		address = net.JoinHostPort(ip.String(), port)
	}

	if up.ProxyEnabled {
		_, d, err := up.config.GetDialerContext(&net.Dialer{
			Timeout: time.Second * time.Duration(up.config.Timeout),
		})
		if err != nil {
			return nil, err
		}
		switch network {
		case "tcp":
			return d.DialContext(ctx, network, address)
		case "tcp-tls":
			conn, err := d.DialContext(ctx, "tcp", address)
			if err != nil {
				return nil, err
			}
			secure := tls.Client(conn, &tls.Config{ServerName: host})
			if err := secure.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return secure, nil
		}
	} else {
		var d net.Dialer
		d.Timeout = time.Second * time.Duration(up.config.Timeout)
		switch network {
		case "tcp":
			return d.DialContext(ctx, network, address)
		case "tcp-tls":
			return (&tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: host}}).DialContext(ctx, "tcp", address)
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
			doh.WithConnectionLimits(up.config.MaxActiveConnections, up.config.MaxIdleConnections),
		}
		if up.ProxyEnabled {
			ops = append(ops, doh.WithSocksProxy(up.config.GetDialerContext))
		}
		up.dohClient = doh.NewClient(ops...)
	}

	// 只需要启用 tcp/tcp-tls 协议的连接池
	if strings.Contains(up.protocol, "tcp") {
		maxIdleTime := time.Second * time.Duration(up.config.Timeout*10)
		timeout := time.Second * time.Duration(up.config.Timeout)
		p := net2.NewSimpleConnectionPool(net2.ConnectionOptions{
			MaxActiveConnections: int32(up.config.MaxActiveConnections),
			MaxIdleConnections:   uint32(up.config.MaxIdleConnections),
			MaxIdleTime:          &maxIdleTime,
			// Do not impose a hidden 20-dial ceiling below the configured active limit.
			DialMaxConcurrency: up.config.MaxActiveConnections,
			ReadTimeout:        timeout,
			WriteTimeout:       timeout,
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
	return up.ExchangeContext(context.Background(), req)
}

func (up *Upstream) ExchangeContext(ctx context.Context, req *dns.Msg) (*dns.Msg, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	up.logger.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Inc(), up.poolLen(), runtime.NumGoroutine(), "enter")
	defer up.logger.Printf("tracing exchange %s worker_count: %d pool_count: %d go_routine: %d --> %s", up.Address, up.count.Dec(), up.poolLen(), runtime.NumGoroutine(), "exit")

	name := GetDomainNameFromDnsMsg(req)
	fallback := up.ecsPrefix.IsValid() && up.ECSBlackholeFallbackEnabled() && name != ""
	if fallback {
		if reason, blackholes, known := up.config.ecsBlackholes.lookup(name); known {
			// Known name: skip the ECS probe that would return a blackhole or REFUSED.
			resp, duration, err := up.exchangeTransport(ctx, up.controlledECSRequest(req, false))
			if err == nil && acceptsECSFallback(reason, resp) {
				reportECSBlackholeFallback(ctx, &ECSBlackholeFallback{Domain: name, Upstream: up.Address, Reason: reason,
					BlackholeAddresses: blackholes, PublicAddresses: answerAddressStrings(resp, false), Remembered: true, QueryType: queryTypeName(req)})
			}
			return resp, duration, err
		}
	}
	// Never forward caller-supplied ECS or mutate shared requests during racing.
	resp, duration, err := up.exchangeTransport(ctx, up.controlledECSRequest(req, true))
	reason := ""
	if fallback && err == nil {
		reason = ecsFallbackReason(resp)
	}
	if reason == "" {
		return resp, duration, err
	}
	// See ecsBlackholePrefixes and ECSFallbackReasonRefused: retry once without
	// ECS and keep the original reply unless the retry actually repairs it.
	plain, plainRTT, plainErr := up.exchangeTransport(ctx, up.controlledECSRequest(req, false))
	duration += plainRTT
	if plainErr == nil && acceptsECSFallback(reason, plain) {
		blackholes := answerAddressStrings(resp, true)
		up.config.ecsBlackholes.remember(name, reason, blackholes)
		up.logger.Printf("ECS %s %v for %s via %s; using answer without ECS", reason, blackholes, name, up.Address)
		reportECSBlackholeFallback(ctx, &ECSBlackholeFallback{Domain: name, Upstream: up.Address, Reason: reason,
			BlackholeAddresses: blackholes, PublicAddresses: answerAddressStrings(plain, false), QueryType: queryTypeName(req)})
		return plain, duration, nil
	}
	if ctx.Err() != nil {
		return nil, duration, ctx.Err()
	}
	return resp, duration, nil
}

// exchangeTransport performs one exchange of an already ECS-controlled request.
func (up *Upstream) exchangeTransport(ctx context.Context, req *dns.Msg) (*dns.Msg, time.Duration, error) {
	if up.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(up.config.Timeout)*time.Second)
		defer cancel()
	}
	var resp *dns.Msg
	var duration time.Duration
	var err error

	switch up.protocol {
	case "https", "http":
		resp, duration, err = up.dohClient.ExchangeContext(ctx, req)
	case "udp":
		client := new(dns.Client)
		client.Timeout = time.Second * time.Duration(up.config.Timeout)
		resp, duration, err = exchangeDNSContext(ctx, client, req, up.hostAndPort)
		if err == nil && resp != nil && resp.Truncated {
			// RFC 7766: retry a truncated UDP response over TCP.
			tcpClient := &dns.Client{Net: "tcp", Timeout: client.Timeout}
			var tcpRTT time.Duration
			resp, tcpRTT, err = exchangeDNSContext(ctx, tcpClient, req, up.hostAndPort)
			duration += tcpRTT
		}
	case "tcp", "tcp-tls":
		conn, errGetConn := up.pool.Get(up.protocol, up.hostAndPort)
		if errGetConn != nil {
			up.logger.Printf("upstream pool failed: %v", errGetConn)
			return nil, 0, errGetConn
		}
		resp, err = dnsExchangeWithConnContext(ctx, conn, req)
		// Peers may close idle/query-limited pooled sessions. The failed exchange
		// discards that connection; retry read-only DNS queries once, not timeouts
		// or UPDATE messages whose effects must never be duplicated.
		if ctx.Err() == nil && req.Opcode == dns.OpcodeQuery && retryableClosedConnection(err) {
			// Another idle pooled connection may also have expired. Retry on
			// a fresh transport rather than drawing another stale pool entry.
			fresh, dialErr := up.dialUpstreamContext(ctx, up.protocol, up.hostAndPort)
			err = dialErr
			if err == nil {
				stopCancellation := closeConnectionOnCancel(ctx, fresh)
				_ = fresh.SetDeadline(time.Now().Add(time.Duration(up.config.Timeout) * time.Second))
				co := &dns.Conn{Conn: fresh}
				err = co.WriteMsg(req)
				if err == nil {
					resp, err = co.ReadMsg()
				}
				stopCancellation()
				_ = fresh.Close()
			}
		}
	default:
		panic(fmt.Sprintf("invalid upstream protocol: %s in address %s", up.protocol, up.Address))
	}

	if ctx.Err() != nil {
		return nil, duration, ctx.Err()
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

	if err != nil {
		up.logger.Printf("upstream exchange failed: %v", err)
	}
	return resp, duration, err
}

func dnsExchangeWithConn(conn net2.ManagedConn, req *dns.Msg) (*dns.Msg, error) {
	return dnsExchangeWithConnContext(context.Background(), conn, req)
}

func dnsExchangeWithConnContext(ctx context.Context, conn net2.ManagedConn, req *dns.Msg) (*dns.Msg, error) {
	// Capture the raw connection before releasing its pool handle. Cancellation
	// must finish before reuse, or it could close another query's connection.
	stopCancellation := closeConnectionOnCancel(ctx, conn.RawConn())
	var resp *dns.Msg
	co := dns.Conn{Conn: conn}
	err := co.WriteMsg(req)
	if err == nil {
		resp, err = co.ReadMsg()
	}
	stopCancellation()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		conn.ReleaseConnection()
	} else {
		conn.DiscardConnection()
	}
	return resp, err
}

// EffectiveECS distinguishes an omitted override (inherit) from an explicit empty override (disable).
func (up *Upstream) EffectiveECS() string {
	if up.ECS != nil {
		return *up.ECS
	}
	if up.config != nil {
		return up.config.ECS
	}
	return ""
}

// ECSBlackholeFallbackEnabled reports the configuration switch; unset means enabled.
func (up *Upstream) ECSBlackholeFallbackEnabled() bool {
	return up.config == nil || up.config.ECSBlackholeFallbackEnabled()
}

func parseECS(value string) (netip.Prefix, error) {
	if value == "" {
		return netip.Prefix{}, nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("invalid ECS CIDR %q", value)
	}
	return prefix.Masked(), nil
}

func (up *Upstream) controlledECSRequest(request *dns.Msg, sendECS bool) *dns.Msg {
	req := request.Copy()
	opt := req.IsEdns0()
	if opt != nil {
		options := opt.Option[:0]
		for _, option := range opt.Option {
			if _, ok := option.(*dns.EDNS0_SUBNET); !ok {
				options = append(options, option)
			}
		}
		opt.Option = options
	}
	if !sendECS || !up.ecsPrefix.IsValid() {
		return req
	}
	if opt == nil {
		req.SetEdns0(1232, false)
		opt = req.IsEdns0()
	}
	family := uint16(2)
	if up.ecsPrefix.Addr().Is4() {
		family = 1
	}
	opt.Option = append(opt.Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: family, SourceNetmask: uint8(up.ecsPrefix.Bits()), SourceScope: 0, Address: net.IP(up.ecsPrefix.Addr().AsSlice())})
	return req
}

func retryableClosedConnection(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
