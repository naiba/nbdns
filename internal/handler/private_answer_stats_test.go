package handler

import (
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

// dnsmasq rebind protection drops private answers relayed from nbdns, so every
// served private answer (cache hits included) is one empty reply at the client.
func TestPrivateAnswerRankingCountsEveryServedPrivateAnswer(t *testing.T) {
	upstream, requests := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg).SetReply(req)
		name := req.Question[0].Name
		hdr := dns.RR_Header{Name: name, Class: dns.ClassINET, Ttl: 300}
		switch {
		case name == "internal.example." && req.Question[0].Qtype == dns.TypeA:
			hdr.Rrtype = dns.TypeA
			resp.Answer = []dns.RR{&dns.A{Hdr: hdr, A: net.ParseIP("10.0.0.1")}}
		case name == "internal.example." && req.Question[0].Qtype == dns.TypeAAAA:
			hdr.Rrtype = dns.TypeAAAA
			resp.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("fd00::1")}}
		case name == "alias.example.":
			hdr.Rrtype = dns.TypeCNAME
			target := dns.RR_Header{Name: "lan.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}
			resp.Answer = []dns.RR{&dns.CNAME{Hdr: hdr, Target: "lan.example."}, &dns.A{Hdr: target, A: net.ParseIP("192.168.0.10")}}
		default:
			hdr.Rrtype = dns.TypeA
			resp.Answer = []dns.RR{&dns.A{Hdr: hdr, A: net.ParseIP("1.1.1.1")}}
		}
		return resp
	})
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })

	for range 2 { // The second answer comes from the cache.
		if resp := h.HandleDnsMsg(testQuery("internal.example."), "192.0.2.10", ""); len(resp.Answer) != 1 {
			t.Fatalf("internal answer = %+v", resp)
		}
	}
	aaaa := new(dns.Msg).SetQuestion("internal.example.", dns.TypeAAAA)
	if resp := h.HandleDnsMsg(aaaa, "192.0.2.20", ""); len(resp.Answer) != 1 {
		t.Fatalf("internal AAAA answer = %+v", resp)
	}
	if resp := h.HandleDnsMsg(testQuery("alias.example."), "192.0.2.10", ""); len(resp.Answer) != 2 {
		t.Fatalf("alias answer = %+v", resp)
	}
	if resp := h.HandleDnsMsg(testQuery("public.example."), "192.0.2.10", ""); len(resp.Answer) != 1 {
		t.Fatalf("public answer = %+v", resp)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("upstream requests = %d, want 4 (second internal A served from cache)", got)
	}

	ranking := recorder.GetSnapshot().TopPrivateAnswerDomains
	if len(ranking) != 2 {
		t.Fatalf("private answer ranking = %+v, want internal and alias only", ranking)
	}
	if ranking[0].Key != "internal.example." || ranking[0].Count != 3 || ranking[0].TopClient != "192.0.2.10" || ranking[0].TopClientCount != 2 || len(ranking[0].TopClients) != 2 {
		t.Errorf("internal ranking = %+v", ranking[0])
	}
	if ranking[1].Key != "alias.example." || ranking[1].Count != 1 {
		t.Errorf("alias ranking = %+v", ranking[1])
	}
}

func TestPrivateAnswerRankingIncludesCustomRecordsAndSkipsBlockedQueries(t *testing.T) {
	recorder := stats.NewStats()
	h := NewHandler(false, nil, "", logger.New(false), recorder)
	if err := h.SetCustomRecords([]model.CustomRecord{{Name: "nas.example", Type: "A", Value: "192.168.0.2"}}); err != nil {
		t.Fatal(err)
	}
	f := filter.New()
	if _, err := f.Load(strings.NewReader("||ads.example^\n")); err != nil {
		t.Fatal(err)
	}
	h.SetFilter(f)

	if resp := h.HandleDnsMsg(testQuery("nas.example."), "192.0.2.10", ""); len(resp.Answer) != 1 {
		t.Fatalf("custom record answer = %+v", resp)
	}
	if resp := h.HandleDnsMsg(testQuery("x.ads.example."), "192.0.2.10", ""); resp.Rcode != dns.RcodeNameError {
		t.Fatalf("blocked answer = %+v", resp)
	}

	ranking := recorder.GetSnapshot().TopPrivateAnswerDomains
	if len(ranking) != 1 || ranking[0].Key != "nas.example." || ranking[0].Count != 1 || ranking[0].TopClient != "192.0.2.10" {
		t.Errorf("private answer ranking = %+v, want only the custom LAN record", ranking)
	}
}
