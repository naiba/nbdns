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

func TestQueryAdmissionRejectsOverloadAndPreservesCacheHits(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	up, calls := testUpstream(t, func(q *dns.Msg) *dns.Msg {
		entered <- struct{}{}
		<-release
		return coalescedDNSAnswer(q)
	})
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{up}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.SetMaxConcurrentQueries(2); err != nil {
		t.Fatal(err)
	}
	if err := h.SetMaxConcurrentQueries(0); err == nil {
		t.Fatal("zero admission capacity accepted")
	}
	for _, name := range []string{"cached.example.", "stale.example."} {
		q := testQuery(name)
		expires := time.Now().Add(time.Minute)
		if name == "stale.example." {
			expires = time.Now().Add(-time.Minute)
		}
		if err := h.builtInCache.Set(namespacedDNSCacheKey(h.cacheNamespace, q), &cache.CachedMsg{Msg: coalescedDNSAnswer(q), Expires: expires}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan *dns.Msg, 2)
	for _, name := range []string{"first.example.", "second.example."} {
		go func() { done <- h.HandleDnsMsg(testQuery(name), "", "") }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("admitted queries did not reach upstream")
		}
	}
	q := testQuery("overloaded.example.")
	r := h.HandleDnsMsg(q, "", "")
	if r.Rcode != dns.RcodeServerFailure || r.Id != q.Id || r.Question[0] != q.Question[0] || calls.Load() != 2 {
		t.Errorf("overload was not rejected: %v calls=%d", r, calls.Load())
	}
	for _, name := range []string{"cached.example.", "stale.example."} {
		q := testQuery(name)
		r := h.HandleDnsMsg(q, "", "")
		if r.Rcode != dns.RcodeSuccess || r.Id != q.Id || len(r.Answer) != 1 || calls.Load() != 2 {
			t.Errorf("cache unavailable during overload: %v", r)
		}
		if name == "stale.example." && r.Answer[0].Header().Ttl != 12 {
			t.Errorf("stale fallback TTL changed: %v", r)
		}
	}
	close(release)
	for range 2 {
		select {
		case r := <-done:
			if r.Rcode != dns.RcodeSuccess {
				t.Errorf("admitted request failed: %v", r)
			}
		case <-time.After(time.Second):
			t.Fatal("admitted request did not finish")
		}
	}
	r = h.HandleDnsMsg(testQuery("recovered.example."), "", "")
	if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 || calls.Load() != 3 || len(h.pendingQueries) != 0 || recorder.FailedQueries.Load() != 2 {
		t.Fatalf("admission slots not restored or stats wrong: response=%v calls=%d stats=%+v", r, calls.Load(), recorder.GetSnapshot())
	}
}
