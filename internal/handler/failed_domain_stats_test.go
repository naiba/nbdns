package handler

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestFailedDomainRankingNamesFailingUpstreamsAndLatencyIsRecorded(t *testing.T) {
	upstream, _ := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		if req.Question[0].Name == "broken.example." {
			return new(dns.Msg).SetRcode(req, dns.RcodeRefused)
		}
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("1.1.1.1")}}
		return resp
	})
	recorder := stats.NewStats()
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), recorder)
	clock := newSteppingClock()
	h.clock = clock.Now

	for _, client := range []string{"192.0.2.10", "192.0.2.10", "192.0.2.20"} {
		if resp := h.HandleDnsMsg(testQuery("broken.example."), client, ""); resp.Rcode != dns.RcodeServerFailure {
			t.Fatalf("broken answer = %+v", resp)
		}
		clock.Advance(failureCacheMaximum + time.Second) // Retry upstream after each failure window.
	}
	if resp := h.HandleDnsMsg(testQuery("working.example."), "192.0.2.10", ""); resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("working answer = %+v", resp)
	}

	snapshot := recorder.GetSnapshot()
	failed := snapshot.TopFailedDomains
	if len(failed) != 1 || failed[0].Key != "broken.example." || failed[0].Count != 3 || failed[0].TopClient != "192.0.2.10" || failed[0].TopClientCount != 2 {
		t.Fatalf("failed ranking = %+v", failed)
	}
	if len(failed[0].Reasons) != 1 || failed[0].Reasons[0] != (stats.ReasonCountJSON{Reason: upstream.Address, Count: 3}) {
		t.Errorf("failure reasons = %+v, want the failing upstream", failed[0].Reasons)
	}
	if snapshot.Recent.Failed != 3 || snapshot.Queries.Failed != 3 {
		t.Errorf("failed counters = recent %d, total %d", snapshot.Recent.Failed, snapshot.Queries.Failed)
	}
	if len(snapshot.Upstreams) != 1 {
		t.Fatalf("upstreams = %+v", snapshot.Upstreams)
	}
	got := snapshot.Upstreams[0]
	if got.RecentQueries != 4 || got.RecentErrors != 3 || got.AvgLatencyMs <= 0 || got.P95LatencyMs <= 0 || got.LastUsed == "" {
		t.Errorf("upstream recent stats = %+v", got)
	}
}

// Upstream.ExchangeContext reports no duration for pooled TCP/DoT exchanges,
// so latency must be measured around the exchange rather than taken from it.
func TestUpstreamLatencyIsMeasuredForTCPUpstreams(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{Listener: listener, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		time.Sleep(30 * time.Millisecond)
		_ = w.WriteMsg(addressAnswer("1.1.1.1")(req))
	})}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	upstream := &model.Upstream{Address: "tcp://" + listener.Addr().String()}
	upstream.Init(&model.Config{Timeout: 2}, logger.New(false))
	upstream.InitConnectionPool(nil)
	recorder := stats.NewStats()
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), recorder)

	if resp := h.HandleDnsMsg(testQuery("slow.example."), "192.0.2.10", ""); resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("tcp answer = %+v", resp)
	}
	got := recorder.GetSnapshot().Upstreams
	if len(got) != 1 || got[0].AvgLatencyMs < 25 || got[0].P95LatencyMs < 25 {
		t.Errorf("tcp upstream latency = %+v, want about 30 ms", got)
	}
}
