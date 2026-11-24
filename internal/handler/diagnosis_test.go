package handler

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func addressAnswer(address string) func(*dns.Msg) *dns.Msg {
	return func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.ParseIP(address)}}
		return resp
	}
}

func TestDiagnoseReportsEveryRouteWithoutTouchingStatsOrCache(t *testing.T) {
	general, generalCalls := testUpstream(t, addressAnswer("1.1.1.1"))
	dedicated, _ := testUpstream(t, addressAnswer("10.0.0.7"))
	dedicated.MatchLists = []string{"https://lists.example/corp.conf"}
	dedicated.SetMatchLists(listedDomains{"app.corp.example.": true})
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{general, dedicated}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.SetCustomRecords([]model.CustomRecord{{Name: "nas.example", Type: "A", Value: "192.168.0.2"}}); err != nil {
		t.Fatal(err)
	}
	f := filter.New()
	if _, err := f.Load(strings.NewReader("||ads.example^\n")); err != nil {
		t.Fatal(err)
	}
	h.SetFilter(f)

	// Dedicated route: only the matching upstream is asked; its LAN answer is flagged.
	got, err := h.Diagnose(context.Background(), " App.Corp.Example ", dns.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "app.corp.example." || got.Type != "A" || got.Route != DiagnosisRouteDedicated || got.Blocked || len(got.Sources) != 1 {
		t.Fatalf("dedicated diagnosis = %+v", got)
	}
	source := got.Sources[0]
	if source.Kind != "upstream" || source.Address != dedicated.Address || source.Rcode != "NOERROR" || !source.RebindBlocked ||
		len(source.Answers) != 1 || source.Answers[0] != "A 10.0.0.7" || source.LatencyMs <= 0 || source.Error != "" {
		t.Errorf("dedicated source = %+v", source)
	}

	// Default route with a cached answer: the cache entry and the upstream are both reported.
	h.HandleDnsMsg(testQuery("www.example."), "192.0.2.1", "")
	before := recorder.GetSnapshot().Queries.Total
	callsBefore := generalCalls.Load()
	got, err = h.Diagnose(context.Background(), "www.example", dns.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Route != DiagnosisRouteDefault || len(got.Sources) != 2 || got.Sources[0].Kind != "cache" || got.Sources[1].Kind != "upstream" {
		t.Fatalf("default diagnosis = %+v", got)
	}
	if cache := got.Sources[0]; cache.TTLSeconds <= 0 || cache.TTLSeconds > 300 || cache.RebindBlocked || len(cache.Answers) != 1 || cache.Answers[0] != "A 1.1.1.1" {
		t.Errorf("cache source = %+v", cache)
	}
	if generalCalls.Load() != callsBefore+1 {
		t.Errorf("diagnosis upstream calls = %d, want one direct query", generalCalls.Load()-callsBefore)
	}
	if after := recorder.GetSnapshot(); after.Queries.Total != before || len(after.TopDomains) != 1 || after.Upstreams[0].TotalQueries != 1 {
		t.Errorf("diagnosis changed statistics: %+v", after.Queries)
	}

	// Custom records and filter rules are reported ahead of upstreams.
	got, _ = h.Diagnose(context.Background(), "nas.example", dns.TypeA)
	if len(got.Sources) != 2 || got.Sources[0].Kind != "custom" || !got.Sources[0].RebindBlocked || got.Sources[0].Answers[0] != "A 192.168.0.2" {
		t.Errorf("custom diagnosis = %+v", got)
	}
	got, _ = h.Diagnose(context.Background(), "x.ads.example", dns.TypeA)
	if !got.Blocked {
		t.Errorf("filter not reported: %+v", got)
	}

	for _, invalid := range []string{"", ".", "bad..name", strings.Repeat("a", 64) + ".example"} {
		if _, err := h.Diagnose(context.Background(), invalid, dns.TypeA); err != ErrInvalidDiagnosisName {
			t.Errorf("Diagnose(%q) error = %v", invalid, err)
		}
	}
}

func TestDiagnoseReportsUpstreamErrors(t *testing.T) {
	failing, _ := testUpstream(t, func(req *dns.Msg) *dns.Msg { return new(dns.Msg).SetRcode(req, dns.RcodeRefused) })
	h := NewHandler(false, []*model.Upstream{failing}, "", logger.New(false), nil)
	got, err := h.Diagnose(context.Background(), "refused.example", dns.TypeAAAA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "AAAA" || len(got.Sources) != 1 || got.Sources[0].Rcode != "REFUSED" || len(got.Sources[0].Answers) != 0 {
		t.Errorf("refused diagnosis = %+v", got)
	}
}
