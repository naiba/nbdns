package handler

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/cache"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

// An expired entry is resolved upstream like a miss. Counting it as a hit
// overstated the hit rate by every expiry of a frequently queried name.
func TestExpiredCacheEntryCountsAsMiss(t *testing.T) {
	upstream, requests := testUpstream(t, addressAnswer("1.1.1.1"))
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })
	q := testQuery("expired.example.")
	if err := h.builtInCache.Set(namespacedDNSCacheKey(h.cacheNamespace, q), &cache.CachedMsg{Msg: addressAnswer("192.0.2.1")(q), Expires: time.Now().Add(-time.Minute)}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if resp := h.HandleDnsMsg(testQuery("expired.example."), "", ""); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "1.1.1.1" {
		t.Fatalf("expired entry answer = %+v", resp)
	}
	if requests.Load() != 1 || recorder.CacheHits.Load() != 0 || recorder.CacheMisses.Load() != 1 {
		t.Errorf("upstream=%d hits=%d misses=%d, want the expired entry counted as a miss", requests.Load(), recorder.CacheHits.Load(), recorder.CacheMisses.Load())
	}
	h.HandleDnsMsg(testQuery("expired.example."), "", "")
	if recorder.CacheHits.Load() != 1 || recorder.CacheMisses.Load() != 1 {
		t.Errorf("fresh entry: hits=%d misses=%d", recorder.CacheHits.Load(), recorder.CacheMisses.Load())
	}
}

func answerWithTTL(ttl uint32) func(*dns.Msg) *dns.Msg {
	return func(req *dns.Msg) *dns.Msg {
		resp := addressAnswer("1.1.1.1")(req)
		resp.Answer[0].Header().Ttl = ttl
		return resp
	}
}

// The cap was a hard-coded hour; long-TTL names were refetched every hour.
func TestCacheLifetimeFollowsConfiguredMaximum(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ttl     uint32
		maximum time.Duration // 0 keeps the default
		want    time.Duration
	}{
		{"default keeps a two-hour TTL", 7200, 0, 2 * time.Hour},
		{"default caps at one day", 3 * 86400, 0, 24 * time.Hour},
		{"configured cap", 7200, 10 * time.Minute, 10 * time.Minute},
		{"short TTL unchanged", 60, 10 * time.Minute, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, _ := testUpstream(t, answerWithTTL(tc.ttl))
			h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
			t.Cleanup(func() { _ = h.Close() })
			if tc.maximum > 0 {
				if err := h.SetCacheMaxTTL(tc.maximum); err != nil {
					t.Fatal(err)
				}
			}
			q := testQuery("ttl.example.")
			h.HandleDnsMsg(q, "", "")
			cached, ok := h.builtInCache.Get(namespacedDNSCacheKey(h.cacheNamespace, testQuery("ttl.example.")))
			if !ok {
				t.Fatal("answer not cached")
			}
			if lifetime := time.Until(cached.Expires); lifetime > tc.want || lifetime < tc.want-5*time.Second {
				t.Errorf("cache lifetime = %v, want %v", lifetime, tc.want)
			}
		})
	}
	h := NewHandler(false, nil, "", logger.New(false), nil)
	if err := h.SetCacheMaxTTL(0); err == nil {
		t.Error("zero cache_max_ttl accepted")
	}
}
