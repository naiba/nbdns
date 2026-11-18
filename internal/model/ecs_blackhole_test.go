package model

import (
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
)

// geoBlockingAuthority mimics geo-blocking authorities: queries carrying an ECS
// from a blocked region receive a link-local blackhole, while queries without ECS receive
// the real public CDN address.
type geoBlockingAuthority struct {
	mu                  sync.Mutex
	withECS, withoutECS int
	answerWithECS       net.IP
	answerWithoutECS    net.IP
}

func (a *geoBlockingAuthority) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	hasECS := false
	if opt := req.IsEdns0(); opt != nil {
		for _, option := range opt.Option {
			if _, ok := option.(*dns.EDNS0_SUBNET); ok {
				hasECS = true
			}
		}
	}
	a.mu.Lock()
	answer := a.answerWithoutECS
	if hasECS {
		a.withECS++
		answer = a.answerWithECS
	} else {
		a.withoutECS++
	}
	a.mu.Unlock()
	resp := new(dns.Msg).SetReply(req)
	name := req.Question[0].Name
	resp.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300}, Target: "edge.example."}}
	if answer.To4() != nil && req.Question[0].Qtype == dns.TypeA {
		resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: "edge.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: answer})
	}
	if answer.To4() == nil && req.Question[0].Qtype == dns.TypeAAAA {
		resp.Answer = append(resp.Answer, &dns.AAAA{Hdr: dns.RR_Header{Name: "edge.example.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 300}, AAAA: answer})
	}
	_ = w.WriteMsg(resp)
}

func (a *geoBlockingAuthority) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withECS, a.withoutECS
}

func startGeoBlockingUpstream(t *testing.T, authority *geoBlockingAuthority, configJSON string) *Upstream {
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

func answerAddresses(msg *dns.Msg) []string {
	var addresses []string
	for _, rr := range msg.Answer {
		switch record := rr.(type) {
		case *dns.A:
			addresses = append(addresses, record.A.String())
		case *dns.AAAA:
			addresses = append(addresses, record.AAAA.String())
		}
	}
	return addresses
}

func TestECSBlackholeFallsBackToQueryWithoutECS(t *testing.T) {
	authority := &geoBlockingAuthority{answerWithECS: net.ParseIP("169.254.0.2"), answerWithoutECS: net.ParseIP("203.0.113.10")}
	up := startGeoBlockingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)

	resp, _, err := up.Exchange(new(dns.Msg).SetQuestion("geo-blocked.example.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if got := answerAddresses(resp); len(got) != 1 || got[0] != "203.0.113.10" {
		t.Fatalf("blackhole answer was not replaced by the ECS-free answer: %v", got)
	}
	if len(resp.Answer) != 2 || resp.Answer[0].Header().Rrtype != dns.TypeCNAME {
		t.Fatalf("CNAME chain lost from fallback answer: %v", resp.Answer)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 1 {
		t.Fatalf("expected one ECS probe and one ECS-free retry, got withECS=%d withoutECS=%d", withECS, withoutECS)
	}

	// The domain is remembered: the next cache miss (including other types of
	// the same name) goes straight to the ECS-free query instead of probing again.
	resp, _, err = up.Exchange(new(dns.Msg).SetQuestion("GEO-BLOCKED.EXAMPLE.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if got := answerAddresses(resp); len(got) != 1 || got[0] != "203.0.113.10" {
		t.Fatalf("remembered domain returned wrong answer: %v", got)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 2 {
		t.Fatalf("remembered domain must skip ECS, got withECS=%d withoutECS=%d", withECS, withoutECS)
	}

	// Memory is shared by every upstream using the same configuration.
	other := &Upstream{Address: up.Address}
	other.Init(up.config, logger.New(false))
	if _, _, err := other.Exchange(new(dns.Msg).SetQuestion("geo-blocked.example.", dns.TypeA)); err != nil {
		t.Fatal(err)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 3 {
		t.Fatalf("memory not shared between upstreams, got withECS=%d withoutECS=%d", withECS, withoutECS)
	}
}

func TestECSBlackholeFallbackHandlesIPv6(t *testing.T) {
	authority := &geoBlockingAuthority{answerWithECS: net.ParseIP("::"), answerWithoutECS: net.ParseIP("2001:db8:20::20")}
	up := startGeoBlockingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	resp, _, err := up.Exchange(new(dns.Msg).SetQuestion("v6.example.", dns.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	if got := answerAddresses(resp); len(got) != 1 || got[0] != "2001:db8:20::20" {
		t.Fatalf("IPv6 blackhole was not replaced: %v", got)
	}
}

func TestECSBlackholeKeepsIntentionalPrivateAnswers(t *testing.T) {
	// A domain that really points at a LAN address resolves privately with and
	// without ECS; the original answer must be kept and the domain not remembered.
	authority := &geoBlockingAuthority{answerWithECS: net.ParseIP("192.168.1.10"), answerWithoutECS: net.ParseIP("192.168.1.10")}
	up := startGeoBlockingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	for i := 0; i < 2; i++ {
		resp, _, err := up.Exchange(new(dns.Msg).SetQuestion("nas.example.", dns.TypeA))
		if err != nil {
			t.Fatal(err)
		}
		if got := answerAddresses(resp); len(got) != 1 || got[0] != "192.168.1.10" {
			t.Fatalf("intentional private answer changed: %v", got)
		}
	}
	if withECS, withoutECS := authority.counts(); withECS != 2 || withoutECS != 2 {
		t.Fatalf("private-only domain must keep using ECS, got withECS=%d withoutECS=%d", withECS, withoutECS)
	}
}

func TestECSBlackholeIgnoresPublicAnswers(t *testing.T) {
	authority := &geoBlockingAuthority{answerWithECS: net.ParseIP("198.51.100.208"), answerWithoutECS: net.ParseIP("198.51.100.10")}
	up := startGeoBlockingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2}`)
	resp, _, err := up.Exchange(new(dns.Msg).SetQuestion("cdn.example.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if got := answerAddresses(resp); len(got) != 1 || got[0] != "198.51.100.208" {
		t.Fatalf("public ECS answer must be kept: %v", got)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 0 {
		t.Fatalf("public answers must not trigger a retry, got withECS=%d withoutECS=%d", withECS, withoutECS)
	}
}

func TestECSBlackholeFallbackCanBeDisabled(t *testing.T) {
	authority := &geoBlockingAuthority{answerWithECS: net.ParseIP("169.254.0.2"), answerWithoutECS: net.ParseIP("203.0.113.10")}
	up := startGeoBlockingUpstream(t, authority, `{"ecs":"180.152.0.0/24","timeout":2,"ecs_blackhole_fallback":false}`)
	resp, _, err := up.Exchange(new(dns.Msg).SetQuestion("geo-blocked.example.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if got := answerAddresses(resp); len(got) != 1 || got[0] != "169.254.0.2" {
		t.Fatalf("disabled fallback must return the upstream answer unchanged: %v", got)
	}
	if withECS, withoutECS := authority.counts(); withECS != 1 || withoutECS != 0 {
		t.Fatalf("disabled fallback must not retry, got withECS=%d withoutECS=%d", withECS, withoutECS)
	}
}

func TestECSBlackholeAddressClassification(t *testing.T) {
	for address, want := range map[string]bool{
		"169.254.0.2": true, "0.0.0.0": true, "0.1.2.3": true, "127.0.0.1": true,
		"10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true, "100.64.0.1": true,
		"198.18.0.1": true, "224.0.0.1": true, "240.0.0.1": true, "255.255.255.255": true,
		"::": true, "::1": true, "fe80::1": true, "fd00::1": true, "ff02::1": true,
		"::ffff:169.254.0.2": true,
		"203.0.113.10":       false, "8.8.8.8": false, "2001:db8:20::20": false, "::ffff:8.8.8.8": false,
	} {
		if got := isECSBlackholeAddress(net.ParseIP(address)); got != want {
			t.Errorf("isECSBlackholeAddress(%s)=%v want %v", address, got, want)
		}
	}
}

func TestECSBlackholeFallbackStatus(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"enabled by default", `{"ecs":"180.152.0.0/24","upstreams":[{"address":"udp://192.0.2.53:53"}]}`, "enabled"},
		{"explicitly disabled", `{"ecs":"180.152.0.0/24","ecs_blackhole_fallback":false,"upstreams":[{"address":"udp://192.0.2.53:53"}]}`, "disabled"},
		{"no ECS anywhere", `{"upstreams":[{"address":"udp://192.0.2.53:53"}]}`, "no_ecs"},
		{"global ECS disabled on every upstream", `{"ecs":"180.152.0.0/24","upstreams":[{"address":"udp://192.0.2.53:53","ecs":""}]}`, "no_ecs"},
		{"ECS only on one upstream", `{"upstreams":[{"address":"udp://192.0.2.53:53"},{"address":"udp://192.0.2.54:53","ecs":"180.152.0.0/24"}]}`, "enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config Config
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatal(err)
			}
			for _, up := range config.Upstreams {
				up.Init(&config, logger.New(false))
			}
			if got := config.ECSBlackholeFallbackStatus(); got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestECSBlackholeMemoryEvictsEarliestExpiryWhenFull(t *testing.T) {
	var memory ecsBlackholeMemory
	for i := 0; i < ecsBlackholeMemoryCapacity; i++ {
		memory.remember("d"+strconv.Itoa(i)+".example.", ECSFallbackReasonBlackhole, nil)
	}
	memory.remember("newest.example.", ECSFallbackReasonBlackhole, []string{"169.254.0.2"})
	if reason, blackholes, ok := memory.lookup("newest.example."); !ok || len(blackholes) != 1 || reason != ECSFallbackReasonBlackhole {
		t.Fatal("a full memory must still remember newly detected domains")
	}
	if _, _, ok := memory.lookup("d0.example."); ok {
		t.Error("the entry expiring first must be evicted")
	}
	if _, _, ok := memory.lookup("d1.example."); !ok {
		t.Error("only one entry may be evicted")
	}
	memory.mu.Lock()
	size := len(memory.entries)
	memory.mu.Unlock()
	if size != ecsBlackholeMemoryCapacity {
		t.Errorf("size = %d, want %d", size, ecsBlackholeMemoryCapacity)
	}
}
