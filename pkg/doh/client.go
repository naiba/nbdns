package doh

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
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
	timeout   time.Duration
	server    string
	bootstrap func(domain string) (net.IP, error)
	getDialer func(d *net.Dialer) (proxy.Dialer, proxy.ContextDialer, error)
	logger    Logger
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

type Client struct {
	opt      *clientOptions
	cli      *http.Client
	traceCtx context.Context
}

func NewClient(opts ...ClientOption) *Client {
	o := new(clientOptions)
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

	var transport *http.Transport

	if o.bootstrap != nil {
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				urls := strings.Split(address, ":")
				ipv4, err := o.bootstrap(urls[0])
				if err != nil {
					return nil, errors.Wrap(err, "bootstrap")
				}
				urls[0] = ipv4.String()

				if o.getDialer != nil {
					dialer, _, err := o.getDialer(&net.Dialer{
						Timeout: o.timeout,
					})
					if err != nil {
						return nil, err
					}
					return dialer.Dial("tcp", strings.Join(urls, ":"))
				}

				return (&net.Dialer{
					Timeout: o.timeout,
				}).DialContext(ctx, network, strings.Join(urls, ":"))
			},
		}
	}

	return &Client{
		opt:      o,
		traceCtx: httptrace.WithClientTrace(context.Background(), clientTrace),
		cli: &http.Client{
			Transport: transport,
			Timeout:   o.timeout,
		},
	}
}

func (c *Client) Exchange(req *dns.Msg) (r *dns.Msg, rtt time.Duration, err error) {
	var (
		buf    []byte
		begin  = time.Now()
		origID = req.Id
		hreq   *http.Request
	)

	// Set DNS ID as zero accoreding to RFC8484 (cache friendly)
	req.Id = 0
	buf, err = req.Pack()
	if err != nil {
		return
	}

	hreq, err = http.NewRequestWithContext(c.traceCtx, http.MethodGet, c.opt.server+"?dns="+base64.RawURLEncoding.EncodeToString(buf), nil)
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
