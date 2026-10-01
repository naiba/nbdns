package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
	"github.com/yl2chen/cidranger"
)

func testUpstream(t *testing.T, reply func(*dns.Msg) *dns.Msg) (*model.Upstream, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		wire, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			t.Errorf("decode DNS query: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		query := new(dns.Msg)
		if err := query.Unpack(wire); err != nil {
			t.Errorf("unpack DNS query: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, err := reply(query).Pack()
		if err != nil {
			t.Errorf("pack DNS response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	config := &model.Config{Timeout: 2}
	upstream := &model.Upstream{Address: server.URL + "/dns-query"}
	upstream.Init(config, cidranger.NewPCTrieRanger(), logger.New(false))
	upstream.InitConnectionPool(nil)
	return upstream, requests
}

func testQuery(name string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	return q
}

func TestUpstreamResponseCodeAndEDNS(t *testing.T) {
	upstream, _ := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeNameError)
		resp.SetEdns0(1232, false)
		return resp
	})
	h := NewHandler(model.StrategyAnyResult, false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	q := testQuery("missing.example.")
	q.SetEdns0(1232, false)
	resp := h.HandleDnsMsg(q, "", "")
	if resp.Rcode != dns.RcodeNameError {
		t.Errorf("Rcode = %d, want NXDOMAIN", resp.Rcode)
	}
	if resp.IsEdns0() == nil {
		t.Error("EDNS query must have an OPT in its response")
	}
}

func TestNegativeCacheUsesSOATTL(t *testing.T) {
	upstream, requests := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeNameError)
		resp.Ns = []dns.RR{&dns.SOA{
			Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 120},
			Ns:  "ns.example.", Mbox: "hostmaster.example.", Minttl: 30,
		}}
		return resp
	})
	h := NewHandler(model.StrategyAnyResult, true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	q := testQuery("missing.example.")
	for i := 0; i < 2; i++ {
		resp := h.HandleDnsMsg(q.Copy(), "", "")
		if resp.Rcode != dns.RcodeNameError {
			t.Errorf("response %d: Rcode = %d, want NXDOMAIN", i, resp.Rcode)
		}
		if len(resp.Ns) != 1 || resp.Ns[0].Header().Ttl > 30 {
			t.Errorf("response %d: SOA TTL exceeds negative cache lifetime: %+v", i, resp.Ns)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1", got)
	}
}

func TestHotCachePreservesClientID(t *testing.T) {
	upstream, requests := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
		}}
		return resp
	})
	h := NewHandler(model.StrategyAnyResult, true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	for _, id := range []uint16{123, 456, 789} {
		q := testQuery("hot.example.")
		q.Id = id
		resp := h.HandleDnsMsg(q, "", "")
		if resp.Id != id || resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
			t.Errorf("client ID %d: %+v", id, resp)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("hot DNS answer reached upstream %d times", got)
	}
	if got := h.GetCacheStats(); !strings.Contains(got, "hits: 2") {
		t.Errorf("hot tier did not record two hits: %s", got)
	}
}

func TestCacheRespectsShortTTLAndRecordTTLs(t *testing.T) {
	msg := new(dns.Msg)
	msg.Answer = []dns.RR{
		&dns.CNAME{Hdr: dns.RR_Header{Name: "a.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 5}, Target: "b.example."},
		&dns.A{Hdr: dns.RR_Header{Name: "b.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}},
	}
	if got := getDnsResponseTtl(msg); got > 5*time.Second || got <= 0 {
		t.Errorf("cache lifetime = %s, want <= 5s", got)
	}
	resp := replyUpdateTtl(testQuery("a.example."), msg.Copy(), 4)
	if resp.Answer[0].Header().Ttl > 4 || resp.Answer[1].Header().Ttl > 300 {
		t.Errorf("cached response extends RR TTLs: %v", resp.Answer)
	}
}

func TestSignedCacheDoesNotOutliveSignature(t *testing.T) {
	msg := new(dns.Msg)
	msg.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "signed.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}},
		&dns.RRSIG{Hdr: dns.RR_Header{Name: "signed.example.", Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 300}, Expiration: uint32(time.Now().Add(3 * time.Second).Unix())},
	}
	if got := getDnsResponseTtl(msg); got > 3*time.Second {
		t.Errorf("cache lifetime = %s, exceeds RRSIG validity", got)
	}
}

func TestUDPResponseRespectsAdvertisedSize(t *testing.T) {
	q := testQuery("big.example.")
	resp := new(dns.Msg).SetReply(q)
	for range 15 {
		resp.Answer = append(resp.Answer, &dns.TXT{
			Hdr: dns.RR_Header{Name: "big.example.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
			Txt: []string{strings.Repeat("x", 100)},
		})
	}
	truncateUDP(q, resp)
	data, err := resp.Pack()
	if err != nil || len(data) > dns.MinMsgSize || !resp.Truncated {
		t.Errorf("UDP response size=%d, truncated=%v, err=%v", len(data), resp.Truncated, err)
	}
}

func TestMalformedQueryReturnsFormatError(t *testing.T) {
	h := NewHandler(model.StrategyAnyResult, false, nil, "", logger.New(false), nil)
	if resp := h.HandleDnsMsg(new(dns.Msg), "", ""); resp.Rcode != dns.RcodeFormatError {
		t.Errorf("empty query Rcode = %d, want FORMERR", resp.Rcode)
	}
}

func TestPrimaryNXDOMAINIsNotDiscarded(t *testing.T) {
	upstream, _ := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		return new(dns.Msg).SetRcode(req, dns.RcodeNameError)
	})
	upstream.IsPrimary = true
	h := NewHandler(model.StrategyFullest, false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	if got := h.HandleDnsMsg(testQuery("unknown.example."), "", "").Rcode; got != dns.RcodeNameError {
		t.Errorf("Rcode = %d, want NXDOMAIN", got)
	}
}

func TestMismatchedUpstreamQuestionIsRejected(t *testing.T) {
	upstream, _ := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg).SetReply(req)
		resp.Question[0].Name = "other.example."
		return resp
	})
	h := NewHandler(model.StrategyFullest, false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	if got := h.HandleDnsMsg(testQuery("requested.example."), "", "").Rcode; got != dns.RcodeServerFailure {
		t.Errorf("Rcode = %d, want SERVFAIL for mismatched question", got)
	}
}

func TestFilterBlocksBeforeCacheAndUpstream(t *testing.T) {
	upstream, requests := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
		}}
		return resp
	})
	f := filter.New()
	if _, err := f.Load(strings.NewReader("||ads.example^\n@@||safe.ads.example^\n")); err != nil {
		t.Fatal(err)
	}
	recorder := stats.NewStats()
	h := NewHandler(model.StrategyAnyResult, false, []*model.Upstream{upstream}, "", logger.New(false), recorder)
	h.SetFilter(f)
	blocked := testQuery("x.ads.example.")
	blocked.SetEdns0(1232, false)
	resp := h.HandleDnsMsg(blocked, "192.0.2.10", "")
	if resp.Rcode != dns.RcodeNameError || resp.IsEdns0() == nil || resp.AuthenticatedData {
		t.Errorf("blocked response: %+v", resp)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("blocked query reached upstream: %d", got)
	}
	if resp := h.HandleDnsMsg(testQuery("safe.ads.example."), "192.0.2.20", ""); resp.Rcode != dns.RcodeSuccess {
		t.Errorf("allowlisted response: %+v", resp)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("allowlisted query upstream count = %d, want 1", got)
	}
	snapshot := recorder.GetSnapshot()
	if len(snapshot.TopBlockedDomains) != 1 || snapshot.TopBlockedDomains[0].Key != "x.ads.example." || snapshot.TopBlockedDomains[0].TopClientCount != 1 {
		t.Errorf("blocked domain stats = %+v", snapshot.TopBlockedDomains)
	}
	if len(snapshot.TopDomains) != 1 || snapshot.TopDomains[0].Key != "safe.ads.example." {
		t.Errorf("resolved domain stats = %+v", snapshot.TopDomains)
	}
}
