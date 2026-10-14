package handler

import (
	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/logger"
	"testing"
)

type blockEveryDomain struct{}

func (blockEveryDomain) Blocked(string) bool { return true }

func TestCustomRecordsOverrideFiltersAndUpstreams(t *testing.T) {
	upstream, requests := testUpstream(t, func(q *dns.Msg) *dns.Msg { return new(dns.Msg).SetRcode(q, dns.RcodeRefused) })
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	h.SetFilter(blockEveryDomain{})
	if err := h.SetCustomRecords([]model.CustomRecord{
		{Name: "service.example", Type: "A", Value: "192.0.2.1", TTL: 60},
		{Name: "SERVICE.example.", Type: "A", Value: "192.0.2.2", TTL: 60},
		{Name: "service.example", Type: "AAAA", Value: "2001:db8::1", TTL: 120},
		{Name: "alias.example", Type: "CNAME", Value: "service.example.", TTL: 30},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		kind    uint16
		answers int
	}{
		{"Service.Example.", dns.TypeA, 2}, {"service.example.", dns.TypeAAAA, 1}, {"service.example.", dns.TypeMX, 0}, {"service.example.", dns.TypeANY, 3}, {"alias.example.", dns.TypeA, 1},
	} {
		q := new(dns.Msg).SetQuestion(tc.name, tc.kind)
		q.SetEdns0(1232, true)
		r := h.HandleDnsMsg(q, "", "")
		if r.Rcode != dns.RcodeSuccess || len(r.Answer) != tc.answers || r.Id != q.Id || r.Question[0] != q.Question[0] || r.AuthenticatedData {
			t.Fatalf("unexpected reply: %v", r)
		}
		if r.IsEdns0() == nil || !r.IsEdns0().Do() {
			t.Fatal("EDNS metadata lost")
		}
		if tc.kind == dns.TypeA && tc.answers == 2 {
			for i, expected := range []string{"192.0.2.1", "192.0.2.2"} {
				if r.Answer[i].(*dns.A).A.String() != expected || r.Answer[i].Header().Ttl != 60 {
					t.Fatal(r)
				}
			}
			r.Answer[0].Header().Ttl = 999
			if h.HandleDnsMsg(q, "", "").Answer[0].Header().Ttl != 60 {
				t.Fatal("response mutated stored record")
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatal("override leaked to upstream")
	}
	h.SetFilter(nil)
	q := new(dns.Msg).SetQuestion("other.example.", dns.TypeA)
	if h.HandleDnsMsg(q, "", "").Rcode != dns.RcodeServerFailure || requests.Load() != 1 {
		t.Fatal("unmatched query did not reach upstream")
	}
}
