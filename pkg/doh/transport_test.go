package doh

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func writeDNSReply(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	wire, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
	if err != nil {
		t.Error(err)
		return
	}
	query := new(dns.Msg)
	if err := query.Unpack(wire); err != nil {
		t.Error(err)
		return
	}
	reply := new(dns.Msg).SetReply(query)
	reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 42)}}
	wire, err = reply.Pack()
	if err != nil {
		t.Error(err)
		return
	}
	w.Header().Set("Content-Type", dohMediaType)
	_, _ = w.Write(wire)
}

func assertDNSExchange(t *testing.T, client *Client) {
	t.Helper()
	query := new(dns.Msg).SetQuestion("transport.example.", dns.TypeA)
	id := query.Id
	reply, _, err := client.Exchange(query)
	if err != nil {
		t.Error(err)
		return
	}
	if reply.Id != id || len(reply.Question) != 1 || reply.Question[0] != query.Question[0] || reply.Rcode != dns.RcodeSuccess || len(reply.Answer) != 1 {
		t.Errorf("invalid DNS response: %v", reply)
		return
	}
	answer, ok := reply.Answer[0].(*dns.A)
	if !ok || !answer.A.Equal(net.IPv4(192, 0, 2, 42)) {
		t.Errorf("invalid address: %v", reply)
	}
}

func TestDoHCustomDialerNegotiatesHTTP2(t *testing.T) {
	var protocol atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.Store(int32(r.ProtoMajor))
		writeDNSReply(t, w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := NewClient(WithServer(server.URL), WithTimeout(2*time.Second), WithBootstrap(func(string) (net.IP, error) { return net.ParseIP("127.0.0.1"), nil }))
	transport := client.cli.Transport.(*http.Transport)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	t.Cleanup(transport.CloseIdleConnections)
	assertDNSExchange(t, client)
	if got := protocol.Load(); got != 2 {
		t.Fatalf("negotiated HTTP/%d, want HTTP/2", got)
	}
}

func TestDoHReusesConnectionsAcrossConcurrentBursts(t *testing.T) {
	const concurrency = 8
	entered := make(chan struct{}, concurrency)
	release := make(chan struct{}, concurrency)
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeDNSReply(t, w, r)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	client := NewClient(WithServer(server.URL), WithTimeout(2*time.Second), WithBootstrap(func(string) (net.IP, error) { return net.ParseIP("127.0.0.1"), nil }))
	t.Cleanup(client.cli.CloseIdleConnections)
	for range 2 {
		done := make(chan struct{}, concurrency)
		for range concurrency {
			go func() { assertDNSExchange(t, client); done <- struct{}{} }()
		}
		for range concurrency {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("concurrent requests did not reach upstream")
			}
		}
		for range concurrency {
			release <- struct{}{}
		}
		for range concurrency {
			<-done
		}
	}
	if got := connections.Load(); got != concurrency {
		t.Fatalf("two bursts opened %d connections, want %d reused connections", got, concurrency)
	}
}
