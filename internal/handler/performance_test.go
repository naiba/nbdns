package handler

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func BenchmarkDNSCacheKey(b *testing.B) {
	q := new(dns.Msg).SetQuestion("WWW.Example.COM.", dns.TypeAAAA)
	q.SetEdns0(1232, true)
	b.ReportAllocs()
	for b.Loop() {
		if getDnsRequestCacheKey(q) != "www.example.com.#28#1#false#DO" {
			b.Fatal("cache key changed")
		}
	}
}

func TestDNSCacheKeyCompatibility(t *testing.T) {
	for _, name := range []string{"a.", "WWW.Example.COM.", strings.Repeat("x", 63) + ".example."} {
		for _, kind := range []uint16{1, 28, 255, 65535} {
			for _, class := range []uint16{1, 3, 65535} {
				for _, cd := range []bool{false, true} {
					for _, do := range []bool{false, true} {
						q := new(dns.Msg).SetQuestion(name, kind)
						q.Question[0].Qclass = class
						q.CheckingDisabled = cd
						suffix := ""
						if do {
							q.SetEdns0(1232, true)
							suffix = "DO"
						}
						want := fmt.Sprintf("%s#%d#%d#%t#%s", strings.ToLower(name), kind, class, cd, suffix)
						if got := namespacedDNSCacheKey("policy:", q); got != "policy:"+want {
							t.Fatalf("namespaced key = %q", got)
						}
						if got := getDnsRequestCacheKey(q); got != want {
							t.Fatalf("got %q want %q", got, want)
						}
					}
				}
			}
		}
	}
}

func TestCachedReplyCompactionPreservesStorage(t *testing.T) {
	q := new(dns.Msg).SetQuestion("Mixed.Example.", dns.TypeA)
	q.SetEdns0(4096, true)
	original := new(dns.Msg).SetReply(q)
	original.Rcode = dns.RcodeNameError
	original.AuthenticatedData = true
	original.Answer = []dns.RR{
		&dns.RRSIG{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeRRSIG, Class: 1, Ttl: 300}, Expiration: 1},
		&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 120}, A: net.IPv4(192, 0, 2, 42)},
	}
	original.Ns = []dns.RR{&dns.NS{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: 1, Ttl: 90}, Ns: "ns.example."}}
	original.Extra = []dns.RR{&dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT, Class: 4096}}, &dns.A{Hdr: dns.RR_Header{Name: "ns.example.", Rrtype: dns.TypeA, Class: 1, Ttl: 60}, A: net.IPv4(192, 0, 2, 53)}}
	wire, err := original.Pack()
	if err != nil {
		t.Fatal(err)
	}
	response := replyUpdateTtl(q, original.Copy(), 30)
	setResponseEDNS(q, response)
	if len(response.Answer) != 1 || response.Answer[0].Header().Ttl != 30 || len(response.Ns) != 1 || response.Ns[0].Header().Ttl != 30 || len(response.Extra) != 2 || response.Extra[0].Header().Rrtype != dns.TypeA || response.Extra[0].Header().Ttl != 30 {
		t.Fatalf("incorrect record compaction: %v", response)
	}
	if response.Rcode != dns.RcodeNameError || !response.AuthenticatedData || response.Id != q.Id || response.Question[0] != q.Question[0] || response.IsEdns0() == nil || response.IsEdns0().UDPSize() != 1232 || !response.IsEdns0().Do() {
		t.Fatalf("metadata changed: %v", response)
	}
	response.Question[0].Name = "changed.example."
	if q.Question[0].Name != "Mixed.Example." {
		t.Fatal("question aliases input")
	}
	after, err := original.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, after) {
		t.Fatal("cached source mutated")
	}
}

func TestSingleUpstreamUsesSameValidityRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*dns.Msg)
		rcode  int
	}{
		{"success", func(r *dns.Msg) {}, dns.RcodeSuccess},
		{"negative", func(r *dns.Msg) { r.Rcode = dns.RcodeNameError }, dns.RcodeNameError},
		{"refused", func(r *dns.Msg) { r.Rcode = dns.RcodeRefused }, dns.RcodeServerFailure},
		{"truncated", func(r *dns.Msg) { r.Truncated = true }, dns.RcodeServerFailure},
		{"wrong_question", func(r *dns.Msg) { r.Question[0].Name = "wrong.example." }, dns.RcodeServerFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg { r := new(dns.Msg).SetReply(q); tc.mutate(r); return r })
			recorder := stats.NewStats()
			h := NewHandler(false, []*model.Upstream{up}, "", logger.New(false), recorder)
			q := testQuery("expected.example.")
			q.SetEdns0(1232, true)
			r := h.HandleDnsMsg(q, "192.0.2.1", "")
			if r.Rcode != tc.rcode || calls.Load() != 1 || r.Id != q.Id || r.Question[0] != q.Question[0] {
				t.Fatalf("unexpected reply or request count: %v / %d", r, calls.Load())
			}
			snapshot := recorder.GetSnapshot()
			if len(snapshot.Upstreams) != 1 || snapshot.Upstreams[0].TotalQueries != 1 {
				t.Fatalf("missing upstream stats: %+v", snapshot)
			}
			expectedErrors := uint64(0)
			if tc.rcode == dns.RcodeServerFailure {
				expectedErrors = 1
			}
			if snapshot.Upstreams[0].Errors != expectedErrors {
				t.Fatalf("incorrect upstream errors: %+v", snapshot.Upstreams)
			}
			if recorder.TotalQueries.Load() != 1 {
				t.Fatalf("missing query stats: %+v", snapshot)
			}
		})
	}
}
