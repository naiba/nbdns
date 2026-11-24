package model

import (
	"encoding/json"
	"net"
	"sync"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
)

// ecsRefusingAuthority mimics GeoDNS names that answer REFUSED whenever the
// query carries ECS, but resolve normally without it.
type ecsRefusingAuthority struct {
	mu                  sync.Mutex
	withECS, withoutECS int
	refuseWithoutECS    bool
}

func (a *ecsRefusingAuthority) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	hasECS := false
	if opt := req.IsEdns0(); opt != nil {
		for _, option := range opt.Option {
			if _, ok := option.(*dns.EDNS0_SUBNET); ok {
				hasECS = true
			}
		}
	}
	a.mu.Lock()
	if hasECS {
		a.withECS++
	} else {
		a.withoutECS++
	}
	refuse := hasECS || a.refuseWithoutECS
	a.mu.Unlock()
	if refuse {
		_ = w.WriteMsg(new(dns.Msg).SetRcode(req, dns.RcodeRefused))
		return
	}
	resp := new(dns.Msg).SetReply(req)
	resp.Answer = []dns.RR{
		&dns.CNAME{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "edge.example."},
		&dns.A{Hdr: dns.RR_Header{Name: "edge.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("203.0.113.20")},
	}
	_ = w.WriteMsg(resp)
}

func (a *ecsRefusingAuthority) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withECS, a.withoutECS
}

func startRefusingUpstream(t *testing.T, authority *ecsRefusingAuthority, configJSON string) *Upstream {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: authority}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	var config Config
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		t.Fatal(err)
	}
	up := &Upstream{Address: "udp://" + conn.LocalAddr().String()}
	up.Init(&config, logger.New(false))
	return up
}

func TestECSRefusedRetriesWithoutECSAndRemembers(t *testing.T) {
	authority := &ecsRefusingAuthority{}
	up := startRefusingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	report := &ECSBlackholeReport{}
	resp, _, err := up.ExchangeContext(WithECSBlackholeReport(t.Context(), report), new(dns.Msg).SetQuestion("refused.example.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(answerAddresses(resp)) != 1 || answerAddresses(resp)[0] != "203.0.113.20" {
		t.Fatalf("REFUSED with ECS must be retried without ECS: rcode=%d answers=%v", resp.Rcode, resp.Answer)
	}
	if report.Fallback == nil || report.Fallback.Reason != ECSFallbackReasonRefused || report.Fallback.Remembered {
		t.Fatalf("fallback report = %+v, want a fresh refused fallback", report.Fallback)
	}
	if got := report.Fallback.PublicAddresses; len(got) != 1 || got[0] != "203.0.113.20" {
		t.Errorf("reported addresses = %v", got)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 1 {
		t.Fatalf("withECS=%d withoutECS=%d, want one probe and one retry", withECS, withoutECS)
	}

	// Remembered: other query types of the name skip the refused ECS probe.
	report = &ECSBlackholeReport{}
	resp, _, err = up.ExchangeContext(WithECSBlackholeReport(t.Context(), report), new(dns.Msg).SetQuestion("REFUSED.example.", dns.TypeAAAA))
	if err != nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("remembered name: rcode=%v err=%v", resp, err)
	}
	if report.Fallback == nil || report.Fallback.Reason != ECSFallbackReasonRefused || !report.Fallback.Remembered {
		t.Errorf("remembered report = %+v", report.Fallback)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 2 {
		t.Errorf("remembered name must skip ECS: withECS=%d withoutECS=%d", withECS, withoutECS)
	}
}

func TestECSRefusedKeptWhenRefusedWithoutECSToo(t *testing.T) {
	authority := &ecsRefusingAuthority{refuseWithoutECS: true}
	up := startRefusingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	report := &ECSBlackholeReport{}
	for i := 0; i < 2; i++ {
		resp, _, err := up.ExchangeContext(WithECSBlackholeReport(t.Context(), report), new(dns.Msg).SetQuestion("policy.example.", dns.TypeA))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Rcode != dns.RcodeRefused {
			t.Fatalf("a name refused regardless of ECS must stay REFUSED, got %d", resp.Rcode)
		}
	}
	if report.Fallback != nil {
		t.Errorf("nothing was repaired: %+v", report.Fallback)
	}
	if withECS, _ := authority.counts(); withECS != 2 {
		t.Errorf("unrepaired name must not be remembered: withECS=%d", withECS)
	}
}

func TestECSRefusedFallbackFollowsSwitch(t *testing.T) {
	authority := &ecsRefusingAuthority{}
	up := startRefusingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2,"ecs_blackhole_fallback":false}`)
	resp, _, err := up.Exchange(new(dns.Msg).SetQuestion("refused.example.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeRefused {
		t.Errorf("disabled fallback must return the upstream REFUSED, got %d", resp.Rcode)
	}
	if _, withoutECS := authority.counts(); withoutECS != 0 {
		t.Errorf("disabled fallback retried %d times", withoutECS)
	}
}

func TestECSBlackholeReportCarriesReason(t *testing.T) {
	authority := &geoBlockingAuthority{answerWithECS: net.ParseIP("169.254.0.2"), answerWithoutECS: net.ParseIP("203.0.113.10")}
	up := startGeoBlockingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	report := &ECSBlackholeReport{}
	if _, _, err := up.ExchangeContext(WithECSBlackholeReport(t.Context(), report), new(dns.Msg).SetQuestion("geo-blocked.example.", dns.TypeA)); err != nil {
		t.Fatal(err)
	}
	if report.Fallback == nil || report.Fallback.Reason != ECSFallbackReasonBlackhole {
		t.Errorf("blackhole report = %+v", report.Fallback)
	}
}

// Repairs of HTTPS or AAAA queries often carry no address; the dashboard needs
// the query type to tell such a repair from a missing answer.
func TestECSFallbackReportCarriesQueryType(t *testing.T) {
	authority := &ecsRefusingAuthority{}
	up := startRefusingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	for _, qtype := range []uint16{dns.TypeHTTPS, dns.TypeHTTPS} { // Second one is served from memory.
		report := &ECSBlackholeReport{}
		if _, _, err := up.ExchangeContext(WithECSBlackholeReport(t.Context(), report), new(dns.Msg).SetQuestion("refused.example.", qtype)); err != nil {
			t.Fatal(err)
		}
		if report.Fallback == nil || report.Fallback.QueryType != "HTTPS" {
			t.Errorf("fallback report = %+v, want query type HTTPS", report.Fallback)
		}
	}
}
