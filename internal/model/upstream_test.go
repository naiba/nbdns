package model

import (
	"context"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dropbox/godropbox/net2"
	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
	"github.com/naiba/nbdns/pkg/utils"
)

func TestUDPTruncationRetriesOverTCP(t *testing.T) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpConn, err := net.Listen("tcp", udpConn.LocalAddr().String())
	if err != nil {
		_ = udpConn.Close()
		t.Fatal(err)
	}
	udp := &dns.Server{PacketConn: udpConn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg).SetReply(req)
		resp.Truncated = true
		_ = w.WriteMsg(resp)
	})}
	tcp := &dns.Server{Listener: tcpConn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
			A:   net.IPv4(192, 0, 2, 1),
		}}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	t.Cleanup(func() { _ = udp.Shutdown(); _ = tcp.Shutdown() })
	up := &Upstream{Address: "udp://" + udpConn.LocalAddr().String()}
	up.Init(&Config{Timeout: 2}, logger.New(false))
	query := new(dns.Msg)
	query.SetQuestion("example.org.", dns.TypeA)
	resp, _, err := up.Exchange(query)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Truncated || len(resp.Answer) != 1 {
		t.Errorf("did not retry truncated UDP answer via TCP: %+v", resp)
	}
}

func TestIsMatch(t *testing.T) {
	var up Upstream
	up.matchSplited = utils.ParseRules([]string{"."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     true,
		".b.a.com.cn.": true,
		"b.a.com.cn.":  true,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{""})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{".a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     true,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"b.d.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		".a.com.":      false,
		"b.d.com.":     true,
		".b.d.com.cn.": false,
		"b.d.com.cn.":  false,
		".c.d.com.":    false,
		"b.d.a.com.":   false,
	}, t)
}

func checkUpstreamMatch(up *Upstream, cases map[string]bool, t *testing.T) {
	for k, v := range cases {
		isMatch := up.IsMatch(k)
		if isMatch != v {
			t.Errorf("Upstream(%s).IsMatch(%s) = %v, want %v", up.matchSplited, k, isMatch, v)
		}
	}
}

func TestTCPUpstreamReconnectsAfterServerQueryLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{Listener: listener, MaxTCPQueries: 1, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg).SetReply(q)
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 30}, A: net.IPv4(192, 0, 2, 42)}}
		_ = w.WriteMsg(r)
	})}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()
	upstream := &Upstream{Address: "tcp://" + listener.Addr().String()}
	upstream.Init(&Config{Timeout: 1, MaxActiveConnections: 2, MaxIdleConnections: 1}, logger.New(false))
	upstream.InitConnectionPool(nil)
	for i := 0; i < 3; i++ {
		q := new(dns.Msg).SetQuestion("reconnect.example.", dns.TypeA)
		r, _, err := upstream.Exchange(q)
		if err != nil {
			t.Fatalf("query %d failed after server closed pooled connection: %v", i, err)
		}
		if r.Id != q.Id || r.Rcode != 0 || len(r.Answer) != 1 || !r.Answer[0].(*dns.A).A.Equal(net.IPv4(192, 0, 2, 42)) {
			t.Fatalf("incorrect response: %v", r)
		}
	}
}

func TestClosedConnectionRetryClassification(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE, fmt.Errorf("wrapped: %w", io.EOF)} {
		if !retryableClosedConnection(err) {
			t.Fatalf("closed connection not retryable: %v", err)
		}
	}
	for _, err := range []error{nil, context.DeadlineExceeded, syscall.ETIMEDOUT, dns.ErrId, dns.ErrBuf} {
		if retryableClosedConnection(err) {
			t.Fatalf("unexpected retry: %v", err)
		}
	}
}

func TestTCPUpstreamRetriesFreshWhenAllIdleConnectionsClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{Listener: listener, MaxTCPQueries: 1, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) { _ = w.WriteMsg(new(dns.Msg).SetReply(q)) })}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()
	upstream := &Upstream{Address: "tcp://" + listener.Addr().String()}
	upstream.Init(&Config{Timeout: 1, MaxActiveConnections: 4, MaxIdleConnections: 4}, logger.New(false))
	upstream.InitConnectionPool(nil)
	connections := make([]net2.ManagedConn, 3)
	for i := range connections {
		connections[i], err = upstream.pool.Get(upstream.protocol, upstream.hostAndPort)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, conn := range connections {
		q := new(dns.Msg).SetQuestion("stale.example.", dns.TypeA)
		if _, err := dnsExchangeWithConn(conn, q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		q := new(dns.Msg).SetQuestion("fresh.example.", dns.TypeA)
		r, _, err := upstream.Exchange(q)
		if err != nil {
			t.Fatal(err)
		}
		if r.Id != q.Id || r.Rcode != 0 || len(r.Question) != 1 || r.Question[0] != q.Question[0] {
			t.Fatalf("incorrect response: %v", r)
		}
	}
}

func TestUpstreamDialCapacityMatchesConfiguredCapacity(t *testing.T) {
	up := &Upstream{Address: "tcp://127.0.0.1:53"}
	up.Init(&Config{Timeout: 1, MaxActiveConnections: 64, MaxIdleConnections: 64}, logger.New(false))
	up.InitConnectionPool(nil)
	// Inspect the configured pool options, not timing-dependent localhost dials.
	options := reflect.ValueOf(up.pool).Elem().FieldByName("options")
	if got := options.FieldByName("DialMaxConcurrency").Int(); got != 64 {
		t.Fatalf("dial capacity %d silently restricts configured capacity 64", got)
	}
}

func TestTCPUpstreamColdBurstUsesConfiguredCapacity(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{Listener: listener, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) { _ = w.WriteMsg(new(dns.Msg).SetReply(q)) })}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	up := &Upstream{Address: "tcp://resolver.example:" + port}
	up.Init(&Config{Timeout: 3, MaxActiveConnections: 64, MaxIdleConnections: 64}, logger.New(false))
	arrived := make(chan struct{}, 64)
	release := make(chan struct{})
	up.InitConnectionPool(func(string) (net.IP, error) { arrived <- struct{}{}; <-release; return net.IPv4(127, 0, 0, 1), nil })
	results := make(chan error, 64)
	for i := 0; i < 64; i++ {
		go func() {
			q := new(dns.Msg).SetQuestion("burst.example.", dns.TypeA)
			r, _, err := up.Exchange(q)
			if err == nil && (r == nil || r.Id != q.Id || r.Rcode != 0 || len(r.Question) != 1 || r.Question[0] != q.Question[0]) {
				err = fmt.Errorf("invalid burst response: %v", r)
			}
			results <- err
		}()
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	reached := 0
collect:
	for reached < 64 {
		select {
		case <-arrived:
			reached++
		case <-timer.C:
			break collect
		}
	}
	close(release)
	for i := 0; i < 64; i++ {
		if err := <-results; err != nil {
			t.Errorf("burst request failed: %v", err)
		}
	}
	if reached != 64 {
		t.Errorf("only %d of 64 configured concurrent dials started", reached)
	}
}

func TestNonQueryDoesNotRetryClosedConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := &dns.Server{Listener: listener, MaxTCPQueries: 1, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) { calls.Add(1); _ = w.WriteMsg(new(dns.Msg).SetReply(q)) })}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()
	up := &Upstream{Address: "tcp://" + listener.Addr().String()}
	up.Init(&Config{Timeout: 1, MaxActiveConnections: 1, MaxIdleConnections: 1}, logger.New(false))
	up.InitConnectionPool(nil)
	if _, _, err := up.Exchange(new(dns.Msg).SetQuestion("query.example.", dns.TypeA)); err != nil {
		t.Fatal(err)
	}
	update := new(dns.Msg).SetUpdate("example.")
	if _, _, err := up.Exchange(update); err == nil {
		t.Fatal("UPDATE unexpectedly succeeded on closed session")
	}
	if calls.Load() != 1 {
		t.Fatalf("non-idempotent message was retried: %d calls", calls.Load())
	}
}
