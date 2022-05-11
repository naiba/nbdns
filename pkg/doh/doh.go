package doh

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/pkg/errors"
)

const (
	dohMediaType               = "application/dns-message"
	httpTimeout  time.Duration = 2 * time.Second
)

type clientOptions struct {
	timeout   time.Duration
	server    string
	bootstrap func(domain string) (net.IP, error)
	debug     bool
}

type ClientOption func(*clientOptions) error

func WithTimeout(t time.Duration) ClientOption {
	return func(o *clientOptions) error {
		o.timeout = t
		return nil
	}
}

func WithDebug(debug bool) ClientOption {
	return func(o *clientOptions) error {
		o.debug = debug
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

func (o clientOptions) Timeout() time.Duration {
	if o.timeout != 0 {
		return o.timeout
	}
	return httpTimeout
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
			if o.debug {
				log.Printf("http conn was reused: %t", info.Reused)
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
				return (&net.Dialer{
					Timeout: httpTimeout,
				}).DialContext(ctx, network, strings.Join(urls, ":"))
			},
		}

	}

	return &Client{
		opt:      o,
		traceCtx: httptrace.WithClientTrace(context.Background(), clientTrace),
		cli: &http.Client{
			Transport: transport,
			Timeout:   o.Timeout(),
		},
	}
}

func (c *Client) Exchange(req *dns.Msg) (r *dns.Msg, rtt time.Duration, err error) {
	var (
		buf, b64 []byte
		begin    = time.Now()
		origID   = req.Id
	)

	// Set DNS ID as zero accoreding to RFC8484 (cache friendly)
	req.Id = 0
	buf, err = req.Pack()
	b64 = make([]byte, base64.RawURLEncoding.EncodedLen(len(buf)))
	if err != nil {
		return
	}
	base64.RawURLEncoding.Encode(b64, buf)

	hreq, _ := http.NewRequestWithContext(c.traceCtx, http.MethodGet, c.opt.server+"?dns="+string(b64), nil)
	hreq.Header.Add("Accept", dohMediaType)
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
