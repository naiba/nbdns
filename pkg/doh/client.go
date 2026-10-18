package doh

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/miekg/dns"
	"github.com/pkg/errors"
	"golang.org/x/net/proxy"
)

const (
	dohMediaType = "application/dns-message"
)

// Logger 定义可选的日志接口
type Logger interface {
	Printf(format string, v ...interface{})
}

type clientOptions struct {
	timeout                            time.Duration
	server                             string
	bootstrap                          func(domain string) (net.IP, error)
	getDialer                          func(d *net.Dialer) (proxy.Dialer, proxy.ContextDialer, error)
	logger                             Logger
	maxConnections, maxIdleConnections int
}

type ClientOption func(*clientOptions) error

func WithTimeout(t time.Duration) ClientOption {
	return func(o *clientOptions) error {
		o.timeout = t
		return nil
	}
}

func WithSocksProxy(getDialer func(d *net.Dialer) (proxy.Dialer, proxy.ContextDialer, error)) ClientOption {
	return func(o *clientOptions) error {
		o.getDialer = getDialer
		return nil
	}
}

func WithServer(server string) ClientOption {
	return func(o *clientOptions) error {
		o.server = server
		return nil
	}
}

func WithBootstrap(resolver func(domain string) (net.IP, error)) ClientOption {
	return func(o *clientOptions) error {
		o.bootstrap = resolver
		return nil
	}
}

func WithLogger(logger Logger) ClientOption {
	return func(o *clientOptions) error {
		o.logger = logger
		return nil
	}
}

// WithConnectionLimits bounds open connections and retains enough idle sessions
// for subsequent bursts against the same resolver.
func WithConnectionLimits(active, idle int) ClientOption {
	return func(o *clientOptions) error {
		if active > 0 {
			o.maxConnections = active
		}
		if idle > 0 {
			o.maxIdleConnections = idle
		}
		return nil
	}
}

type Client struct {
	opt   *clientOptions
	cli   *http.Client
	trace *httptrace.ClientTrace
}

func NewClient(opts ...ClientOption) *Client {
	o := &clientOptions{maxConnections: 50, maxIdleConnections: 20}
	for _, f := range opts {
		f(o)
	}

	clientTrace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if o.logger != nil {
				o.logger.Printf("http conn was reused: %t", info.Reused)
			}
		},
	}

	// A custom dialer disables automatic HTTP/2 unless explicitly enabled.
	// Keep more than the default two idle HTTP/1 connections: otherwise every
	// concurrent burst repeats TCP/TLS setup even with keep-alive enabled.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	transport.MaxConnsPerHost = o.maxConnections
	transport.MaxIdleConnsPerHost = min(o.maxIdleConnections, o.maxConnections)
	transport.MaxIdleConns = transport.MaxIdleConnsPerHost
	if o.bootstrap != nil || o.getDialer != nil {
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if o.bootstrap != nil && net.ParseIP(host) == nil {
				ip, err := o.bootstrap(host)
				if err != nil {
					return nil, errors.Wrap(err, "bootstrap")
				}
				address = net.JoinHostPort(ip.String(), port)
			}
			dialer := &net.Dialer{Timeout: o.timeout}
			if o.getDialer != nil {
				_, proxyDialer, err := o.getDialer(dialer)
				if err != nil {
					return nil, err
				}
				return proxyDialer.DialContext(ctx, "tcp", address)
			}
			return dialer.DialContext(ctx, network, address)
		}
	}

	return &Client{
		opt:   o,
		trace: clientTrace,
		cli: &http.Client{
			Transport: transport,
			Timeout:   o.timeout,
		},
	}
}

func (c *Client) Exchange(req *dns.Msg) (*dns.Msg, time.Duration, error) {
	return c.ExchangeContext(context.Background(), req)
}

func (c *Client) ExchangeContext(ctx context.Context, req *dns.Msg) (r *dns.Msg, rtt time.Duration, err error) {
	var (
		buf    []byte
		begin  = time.Now()
		origID = req.Id
		hreq   *http.Request
	)

	// Set DNS ID as zero accoreding to RFC8484 (cache friendly)
	query := *req
	query.Id = 0
	buf, err = query.Pack()
	if err != nil {
		return
	}

	hreq, err = http.NewRequestWithContext(httptrace.WithClientTrace(ctx, c.trace), http.MethodGet, c.opt.server+"?dns="+base64.RawURLEncoding.EncodeToString(buf), nil)
	if err != nil {
		return
	}
	hreq.Header.Add("Accept", dohMediaType)
	hreq.Header.Add("User-Agent", "nbdns-doh-client/0.1")

	resp, err := c.cli.Do(hreq)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	if resp.StatusCode != http.StatusOK {
		err = errors.New("DoH query failed: " + string(content))
		return
	}

	r = new(dns.Msg)
	err = r.Unpack(content)
	r.Id = origID
	rtt = time.Since(begin)
	return
}
