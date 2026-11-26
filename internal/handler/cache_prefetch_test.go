package handler

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/cache"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func seedCache(t *testing.T, h *Handler, name string, ttl uint32, remaining time.Duration) string {
	t.Helper()
	q := testQuery(name)
	key := namespacedDNSCacheKey(h.cacheNamespace, q)
	msg := answerWithTTL(ttl)(q)
	msg.Answer[0].(*dns.A).A = []byte{192, 0, 2, 1}
	if err := h.builtInCache.Set(key, &cache.CachedMsg{Msg: msg, Expires: time.Now().Add(remaining)}, remaining+time.Hour); err != nil {
		t.Fatal(err)
	}
	return key
}

func waitFor(t *testing.T, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return condition()
}

func TestPrefetchRefreshesEntriesInTheirLastTenthOfTTL(t *testing.T) {
	upstream, requests := testUpstream(t, answerWithTTL(100))
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })
	key := seedCache(t, h, "hot.example.", 100, 5*time.Second)

	// The client gets the cached answer immediately; the refresh runs behind it.
	resp := h.HandleDnsMsg(testQuery("hot.example."), "", "")
	if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "192.0.2.1" || resp.Answer[0].Header().Ttl > 5 {
		t.Fatalf("served answer = %+v", resp)
	}
	refreshed := waitFor(t, func() bool {
		cached, ok := h.builtInCache.Get(key)
		return ok && time.Until(cached.Expires) > 90*time.Second && cached.Msg.Answer[0].(*dns.A).A.String() == "1.1.1.1"
	})
	if !refreshed || requests.Load() != 1 {
		t.Fatalf("entry not refreshed: refreshed=%v upstream=%d", refreshed, requests.Load())
	}
	if !waitFor(t, func() bool { return recorder.GetSnapshot().Queries.Prefetched == 1 }) {
		t.Errorf("prefetch not counted: %+v", recorder.GetSnapshot().Queries)
	}
	snapshot := recorder.GetSnapshot()
	if snapshot.Queries.CacheHits != 1 || snapshot.Queries.CacheMisses != 0 || snapshot.Recent.Prefetched != 1 {
		t.Errorf("stats = %+v / %+v", snapshot.Queries, snapshot.Recent)
	}
}

func TestPrefetchSkipsFreshAndShortLivedEntries(t *testing.T) {
	upstream, requests := testUpstream(t, answerWithTTL(100))
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	seedCache(t, h, "fresh.example.", 100, 60*time.Second)     // 60% of the TTL left
	seedCache(t, h, "short.example.", 5, 400*time.Millisecond) // TTL below the 10 s minimum
	for _, name := range []string{"fresh.example.", "short.example."} {
		if resp := h.HandleDnsMsg(testQuery(name), "", ""); len(resp.Answer) != 1 {
			t.Fatalf("%s answer = %+v", name, resp)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if requests.Load() != 0 {
		t.Errorf("upstream queried %d times, want no prefetch", requests.Load())
	}
}

func TestPrefetchRunsOncePerEntryAndCloseWaitsForIt(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	upstream, _ := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		calls.Add(1)
		<-release
		return answerWithTTL(100)(req)
	})
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	seedCache(t, h, "busy.example.", 100, 5*time.Second)
	for range 5 {
		h.HandleDnsMsg(testQuery("busy.example."), "", "")
	}
	if !waitFor(t, func() bool { return calls.Load() == 1 }) {
		t.Fatalf("upstream calls = %d, want one refresh", calls.Load())
	}
	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while a refresh was still writing to the cache")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return after the refresh finished")
	}
	if calls.Load() != 1 {
		t.Errorf("upstream calls = %d, want 1", calls.Load())
	}
	// No refresh may start after Close.
	h.maybePrefetch(testQuery("late.example."), "late", &cache.CachedMsg{Msg: answerWithTTL(100)(testQuery("late.example.")), Expires: time.Now().Add(time.Second)})
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Errorf("refresh started after Close")
	}
}

func TestPrefetchLeavesAdmissionCapacityToClients(t *testing.T) {
	upstream, requests := testUpstream(t, answerWithTTL(100))
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.SetMaxConcurrentQueries(1); err != nil {
		t.Fatal(err)
	}
	seedCache(t, h, "hot.example.", 100, 5*time.Second)
	h.pendingQueries <- struct{}{} // A client query holds the only slot.
	h.HandleDnsMsg(testQuery("hot.example."), "", "")
	time.Sleep(100 * time.Millisecond)
	<-h.pendingQueries
	if requests.Load() != 0 {
		t.Errorf("prefetch took the admission slot: upstream=%d", requests.Load())
	}
}
