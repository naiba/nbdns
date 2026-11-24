package handler

import (
	"net"
	"testing"

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

	for _, client := range []string{"192.0.2.10", "192.0.2.10", "192.0.2.20"} {
		if resp := h.HandleDnsMsg(testQuery("broken.example."), client, ""); resp.Rcode != dns.RcodeServerFailure {
			t.Fatalf("broken answer = %+v", resp)
		}
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
