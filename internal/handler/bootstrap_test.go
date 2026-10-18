package handler

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestBootstrapLookupCachesUntilTTL(t *testing.T) {
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		r := new(dns.Msg).SetReply(q)
		last := byte(10)
		if q.Question[0].Name == "other.example." {
			last = 20
		}
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, last)}}
		return r
	})
	h := bootstrapHandlerForTest(t, []*model.Upstream{up}, nil)
	t.Cleanup(func() { _ = h.Close() })
	for _, host := range []string{"resolver.example", "resolver.example", "other.example", "resolver.example"} {
		ip, err := h.LookupIP(host)
		last := byte(10)
		if host == "other.example" {
			last = 20
		}
		if err != nil || !ip.Equal(net.IPv4(192, 0, 2, last)) {
			t.Fatalf("%s: ip=%v err=%v", host, ip, err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("bootstrap queries=%d, want 2 (one per hostname within TTL)", got)
	}
	key := namespacedDNSCacheKey(h.cacheNamespace, testQuery("resolver.example."))
	cached, ok := h.builtInCache.Get(key)
	if !ok || cached == nil || !cached.Expires.After(time.Now()) || time.Until(cached.Expires) > time.Minute {
		t.Fatalf("bootstrap answer did not retain its TTL: %+v", cached)
	}
	// Expire a private entry explicitly; a one-second TTL can cross Badger's
	// timestamp boundary during the initial lookups on a busy CI runner.
	expired := *cached
	expired.Expires = time.Now().Add(-time.Second)
	if err := h.builtInCache.Set(key, &expired, time.Hour); err != nil {
		t.Fatal(err)
	}
	ip, err := h.LookupIP("resolver.example")
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 10)) || calls.Load() != 3 {
		t.Fatalf("expired lookup: ip=%v err=%v calls=%d", ip, err, calls.Load())
	}
}

func TestBootstrapDoesNotCacheZeroTTLOrFailures(t *testing.T) {
	for _, code := range []int{dns.RcodeSuccess, dns.RcodeServerFailure} {
		t.Run(dns.RcodeToString[code], func(t *testing.T) {
			up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
				r := new(dns.Msg).SetRcode(q, code)
				if code == dns.RcodeSuccess {
					r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
				}
				return r
			})
			h := bootstrapHandlerForTest(t, []*model.Upstream{up}, nil)
			t.Cleanup(func() { _ = h.Close() })
			for range 2 {
				ip, err := h.LookupIP("resolver.example")
				if code == dns.RcodeSuccess && (err != nil || !ip.Equal(net.IPv4(192, 0, 2, 1))) {
					t.Fatalf("ip=%v err=%v", ip, err)
				}
				if code != dns.RcodeSuccess && (err == nil || ip != nil) {
					t.Fatalf("failure accepted: ip=%v err=%v", ip, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("queries=%d, want 2", calls.Load())
			}
		})
	}
}

func TestBootstrapCachesNegativeSOAAndBypassesLiteralIPs(t *testing.T) {
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		r := new(dns.Msg).SetRcode(q, dns.RcodeNameError)
		r.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60}, Ns: "ns.example.", Mbox: "hostmaster.example.", Minttl: 30}}
		return r
	})
	h := bootstrapHandlerForTest(t, []*model.Upstream{up}, nil)
	defer h.Close()
	for range 2 {
		ip, err := h.LookupIP("missing.example")
		if err == nil || ip != nil {
			t.Fatalf("negative answer accepted: ip=%v err=%v", ip, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("negative lookups=%d, want 1", calls.Load())
	}
	for _, literal := range []string{"192.0.2.53", "2001:db8::53"} {
		ip, err := h.LookupIP(literal)
		if err != nil || !ip.Equal(net.ParseIP(literal)) {
			t.Fatalf("literal %s: ip=%v err=%v", literal, ip, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("literal IP triggered bootstrap: calls=%d", calls.Load())
	}
}

func bootstrapHandlerForTest(t *testing.T, upstreams []*model.Upstream, records []model.CustomRecord) *Handler {
	t.Helper()
	owner := NewHandler(true, upstreams, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = owner.Close() })
	if err := owner.SetCustomRecords(records); err != nil {
		t.Fatal(err)
	}
	return owner.NewBootstrapHandler(upstreams)
}

func TestBootstrapUsesSharedCustomRecords(t *testing.T) {
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		r := new(dns.Msg).SetReply(q)
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 99)}}
		return r
	})
	records := []model.CustomRecord{
		{Name: "Resolver.Example", Type: "A", Value: "192.0.2.53", TTL: 300},
		{Name: "ipv6.example", Type: "AAAA", Value: "2001:db8::53", TTL: 300},
	}
	bootstrap := bootstrapHandlerForTest(t, []*model.Upstream{up}, records)
	defer bootstrap.Close()
	normal := NewHandler(false, []*model.Upstream{up}, "", logger.New(false), nil)
	defer normal.Close()
	if err := normal.SetCustomRecords(records); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"resolver.example", "RESOLVER.EXAMPLE."} {
		ip, err := bootstrap.LookupIP(name)
		if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) {
			t.Errorf("bootstrap override: ip=%v err=%v", ip, err)
		}
		reply := normal.HandleDnsMsg(testQuery(name), "", "")
		if len(reply.Answer) != 1 {
			t.Fatalf("ordinary override answer=%v", reply)
		}
		answer, ok := reply.Answer[0].(*dns.A)
		if !ok || !answer.A.Equal(net.IPv4(192, 0, 2, 53)) {
			t.Errorf("ordinary override address=%v", reply.Answer)
		}
	}
	// A configured owner without A records stays local, matching ordinary DNS
	// overrides; bootstrap must not silently query a different address upstream.
	if ip, err := bootstrap.LookupIP("ipv6.example"); err == nil || ip != nil {
		t.Errorf("missing A override: ip=%v err=%v", ip, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("configured owners reached bootstrap server %d times", calls.Load())
	}
	for range 2 {
		ip, err := bootstrap.LookupIP("uncustomized.example")
		if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 99)) {
			t.Fatalf("uncustomized lookup: ip=%v err=%v", ip, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unconfigured name did not use TTL cache: calls=%d", calls.Load())
	}
}

func TestBootstrapSharesPersistentCacheAndLifetime(t *testing.T) {
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		r := new(dns.Msg).SetReply(q)
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 53)}}
		return r
	})
	directory := t.TempDir()
	owner := NewHandler(true, []*model.Upstream{up}, directory, logger.New(false), nil)
	defer func() {
		if owner != nil {
			_ = owner.Close()
		}
	}()
	bootstrap := owner.NewBootstrapHandler([]*model.Upstream{up})
	if bootstrap.builtInCache != owner.builtInCache {
		t.Error("bootstrap allocated a separate cache")
	}
	ip, err := bootstrap.LookupIP("resolver.example")
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) {
		t.Fatalf("bootstrap: ip=%v err=%v", ip, err)
	}
	// Closing a borrower must not close the shared Badger instance.
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	assertReply := func(h *Handler, name string) {
		t.Helper()
		r := h.HandleDnsMsg(testQuery(dns.Fqdn(name)), "", "")
		if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
			t.Fatalf("response=%v", r)
		}
		if a, ok := r.Answer[0].(*dns.A); !ok || !a.A.Equal(net.IPv4(192, 0, 2, 53)) {
			t.Fatalf("answer=%v", r.Answer)
		}
	}
	assertReply(owner, "resolver.example")
	if calls.Load() != 1 {
		t.Errorf("same-policy query did not reuse bootstrap answer: calls=%d", calls.Load())
	}
	assertReply(owner, "ordinary.example")
	ip, err = bootstrap.LookupIP("ordinary.example")
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) || calls.Load() != 2 {
		t.Errorf("bootstrap did not reuse ordinary answer: ip=%v err=%v calls=%d", ip, err, calls.Load())
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = nil
	reopened := NewHandler(true, []*model.Upstream{up}, directory, logger.New(false), nil)
	defer reopened.Close()
	ip, err = reopened.NewBootstrapHandler([]*model.Upstream{up}).LookupIP("resolver.example")
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) || calls.Load() != 2 {
		t.Fatalf("bootstrap answer lost after reopening cache: ip=%v err=%v calls=%d", ip, err, calls.Load())
	}
}

func TestBootstrapFollowsSharedCacheSwitch(t *testing.T) {
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		r := new(dns.Msg).SetReply(q)
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 53)}}
		return r
	})
	owner := NewHandler(false, []*model.Upstream{up}, "", logger.New(false), nil)
	defer owner.Close()
	bootstrap := owner.NewBootstrapHandler([]*model.Upstream{up})
	defer bootstrap.Close()
	if bootstrap.builtInCache != nil {
		t.Error("bootstrap ignored built_in_cache=false")
	}
	for range 2 {
		ip, err := bootstrap.LookupIP("resolver.example")
		if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) {
			t.Fatalf("ip=%v err=%v", ip, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("disabled cache retained bootstrap answer: calls=%d", calls.Load())
	}
}

func TestSharedBootstrapCacheSeparatesECSAnswers(t *testing.T) {
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		last := byte(53)
		if opt := q.IsEdns0(); opt != nil {
			for _, option := range opt.Option {
				if _, ok := option.(*dns.EDNS0_SUBNET); ok {
					last = 54
				}
			}
		}
		r := new(dns.Msg).SetReply(q)
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, last)}}
		return r
	})
	owner := NewHandler(true, []*model.Upstream{up}, t.TempDir(), logger.New(false), nil)
	defer owner.Close()
	ip, err := owner.LookupIP("resolver.example")
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) {
		t.Fatalf("default ECS answer: ip=%v err=%v", ip, err)
	}
	ecs := "192.0.2.0/24"
	bootstrapUpstream := &model.Upstream{Address: up.Address, ECS: &ecs}
	bootstrapUpstream.Init(&model.Config{Timeout: 2}, logger.New(false))
	bootstrapUpstream.InitConnectionPool(nil)
	bootstrap := owner.NewBootstrapHandler([]*model.Upstream{bootstrapUpstream})
	if bootstrap.builtInCache != owner.builtInCache {
		t.Fatal("different ECS allocated a separate cache")
	}
	for range 2 {
		ip, err := bootstrap.LookupIP("resolver.example")
		if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 54)) {
			t.Fatalf("bootstrap ECS answer: ip=%v err=%v", ip, err)
		}
	}
	ip, err = owner.LookupIP("resolver.example")
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 53)) || calls.Load() != 2 {
		t.Fatalf("ECS answers mixed or not cached: ip=%v err=%v calls=%d", ip, err, calls.Load())
	}
}
