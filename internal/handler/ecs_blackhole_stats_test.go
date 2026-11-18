package handler

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

// geoBlockingUDPUpstream answers geo-blocked.example ECS queries with 169.254.0.2,
// as geo-blocking authorities do for blocked regions; everything else is public.
func geoBlockingUDPUpstream(t *testing.T, config *model.Config) *model.Upstream {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		answer := net.ParseIP("203.0.113.10")
		if opt := req.IsEdns0(); opt != nil && req.Question[0].Name == "geo-blocked.example." {
			for _, option := range opt.Option {
				if _, ok := option.(*dns.EDNS0_SUBNET); ok {
					answer = net.ParseIP("169.254.0.2")
				}
			}
		}
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: answer}}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	up := &model.Upstream{Address: "udp://" + conn.LocalAddr().String()}
	up.Init(config, logger.New(false))
	return up
}

func TestECSBlackholeFixRecordedOnceForWinningAnswer(t *testing.T) {
	var config model.Config
	if err := json.Unmarshal([]byte(`{"ecs":"180.152.0.0/24","timeout":2}`), &config); err != nil {
		t.Fatal(err)
	}
	// Two racing upstreams both repair the answer; only the winner may count.
	upstreams := []*model.Upstream{geoBlockingUDPUpstream(t, &config), geoBlockingUDPUpstream(t, &config)}
	recorder := stats.NewStats()
	h := NewHandler(false, upstreams, t.TempDir(), logger.New(false), recorder)

	for i, wantFixed := range []uint64{1, 2} {
		resp := h.HandleDnsMsg(new(dns.Msg).SetQuestion("geo-blocked.example.", dns.TypeA), "192.0.2.10", "")
		if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "203.0.113.10" {
			t.Fatalf("query %d answer = %v", i, resp.Answer)
		}
		got := recorder.GetSnapshot().ECSBlackhole
		if got.Fixed != wantFixed {
			t.Fatalf("query %d fixed = %d, want %d", i, got.Fixed, wantFixed)
		}
		if len(got.Domains) != 1 || got.Domains[0].Domain != "geo-blocked.example." || got.Domains[0].Count != wantFixed ||
			len(got.Domains[0].BlackholeAddresses) != 1 || got.Domains[0].BlackholeAddresses[0] != "169.254.0.2" ||
			len(got.Domains[0].PublicAddresses) != 1 || got.Domains[0].PublicAddresses[0] != "203.0.113.10" {
			t.Fatalf("query %d domain stats = %+v", i, got.Domains)
		}
	}

	// Ordinary answers never show up as fixes.
	if resp := h.HandleDnsMsg(new(dns.Msg).SetQuestion("www.example.com.", dns.TypeA), "192.0.2.10", ""); len(resp.Answer) != 1 {
		t.Fatalf("ordinary answer = %v", resp.Answer)
	}
	if got := recorder.GetSnapshot().ECSBlackhole.Fixed; got != 2 {
		t.Errorf("ordinary answer counted as fix: %d", got)
	}
}

func TestECSBlackholeFixNotRecountedOnCacheHit(t *testing.T) {
	var config model.Config
	if err := json.Unmarshal([]byte(`{"ecs":"180.152.0.0/24","timeout":2}`), &config); err != nil {
		t.Fatal(err)
	}
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{geoBlockingUDPUpstream(t, &config)}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })
	for range 3 {
		resp := h.HandleDnsMsg(new(dns.Msg).SetQuestion("geo-blocked.example.", dns.TypeA), "192.0.2.10", "")
		if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "203.0.113.10" {
			t.Fatalf("answer = %v", resp.Answer)
		}
	}
	if got := recorder.GetSnapshot().ECSBlackhole.Fixed; got != 1 {
		t.Errorf("cached repaired answer recounted: fixed = %d, want 1", got)
	}
}

// GeoDNS names that refuse any query carrying ECS used to reach clients as
// SERVFAIL; they must be resolved without ECS and recorded as refused.
func TestECSRefusedNameResolvesInsteadOfServfail(t *testing.T) {
	var config model.Config
	if err := json.Unmarshal([]byte(`{"ecs":"180.152.0.0/24","timeout":2}`), &config); err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		if opt := req.IsEdns0(); opt != nil {
			for _, option := range opt.Option {
				if _, ok := option.(*dns.EDNS0_SUBNET); ok {
					_ = w.WriteMsg(new(dns.Msg).SetRcode(req, dns.RcodeRefused))
					return
				}
			}
		}
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("203.0.113.20")}}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	up := &model.Upstream{Address: "udp://" + conn.LocalAddr().String()}
	up.Init(&config, logger.New(false))
	recorder := stats.NewStats()
	h := NewHandler(false, []*model.Upstream{up}, t.TempDir(), logger.New(false), recorder)

	resp := h.HandleDnsMsg(new(dns.Msg).SetQuestion("refused.example.", dns.TypeA), "192.0.2.10", "")
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("rcode=%d answers=%v, want the answer without ECS", resp.Rcode, resp.Answer)
	}
	got := recorder.GetSnapshot().ECSBlackhole
	if got.Fixed != 1 || len(got.Domains) != 1 || got.Domains[0].Reason != "refused" || len(got.Domains[0].BlackholeAddresses) != 0 {
		t.Errorf("stats = %+v", got)
	}
}
