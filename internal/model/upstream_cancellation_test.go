package model

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestUpstreamCancellationInterruptsDNSRead(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			server := &dns.Server{Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
				if q.Question[0].Name == "blocked.example." {
					close(entered)
					<-release
					return
				}
				_ = w.WriteMsg(new(dns.Msg).SetReply(q))
			})}
			var address string
			var err error
			if network == "udp" {
				server.PacketConn, err = net.ListenPacket(network, "127.0.0.1:0")
				if err == nil {
					address = server.PacketConn.LocalAddr().String()
				}
			} else {
				server.Listener, err = net.Listen(network, "127.0.0.1:0")
				if err == nil {
					address = server.Listener.Addr().String()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = server.ActivateAndServe() }()
			t.Cleanup(func() { close(release); _ = server.Shutdown() })
			up := &Upstream{Address: network + "://" + address}
			up.Init(&Config{Timeout: 2, MaxActiveConnections: 1, MaxIdleConnections: 1}, logger.New(false))
			up.InitConnectionPool(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, _, err := up.ExchangeContext(ctx, new(dns.Msg).SetQuestion("blocked.example.", dns.TypeA))
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("query did not reach upstream")
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("canceled DNS read remained blocked")
			}
			if up.poolLen() != 0 {
				t.Fatalf("canceled connection retained in pool: %d", up.poolLen())
			}
			// Canceling a context after a successful exchange must not close a
			// socket that has already been returned to the pool for reuse.
			for range 3 {
				ctx, cancel := context.WithCancel(context.Background())
				q := new(dns.Msg).SetQuestion("healthy.example.", dns.TypeA)
				r, _, err := up.ExchangeContext(ctx, q)
				cancel()
				if err != nil || r == nil || r.Id != q.Id || r.Rcode != dns.RcodeSuccess || len(r.Question) != 1 || r.Question[0] != q.Question[0] {
					t.Fatalf("subsequent query failed: response=%v error=%v", r, err)
				}
			}
			if network == "tcp" && (up.pool.NumIdle() != 1 || up.pool.NumActive() != 0) {
				t.Fatalf("successful connection not reusable: idle=%d active=%d", up.pool.NumIdle(), up.pool.NumActive())
			}
		})
	}
}
