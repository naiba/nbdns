package handler

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/cache"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

type steppingClock struct{ now atomic.Int64 }

func (c *steppingClock) Now() time.Time          { return time.Unix(0, c.now.Load()) }
func (c *steppingClock) Advance(d time.Duration) { c.now.Add(int64(d)) }
func newSteppingClock() *steppingClock {
	c := &steppingClock{}
	c.now.Store(time.Now().UnixNano())
	return c
}

func failingUpstream(t *testing.T, fail *atomic.Bool) (*model.Upstream, *atomic.Int32) {
	return testUpstream(t, func(req *dns.Msg) *dns.Msg {
		if fail.Load() {
			return new(dns.Msg).SetRcode(req, dns.RcodeServerFailure)
		}
		return addressAnswer("1.1.1.1")(req)
	})
}

// A client retrying a failing name used to send every retry upstream.
// RFC 9520 caches resolution failures briefly.
func TestResolutionFailuresAreCachedWithBackoff(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	upstream, requests := failingUpstream(t, &fail)
	recorder := stats.NewStats()
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), recorder)
	clock := newSteppingClock()
	h.clock = clock.Now

	query := func() int {
		return h.HandleDnsMsg(testQuery("broken.example."), "192.0.2.10", "").Rcode
	}
	for range 5 {
		if rcode := query(); rcode != dns.RcodeServerFailure {
			t.Fatalf("rcode = %d, want SERVFAIL", rcode)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d, want 1 while the failure is cached", requests.Load())
	}

	clock.Advance(failureCacheInitial + time.Millisecond) // First window (5 s) over.
	query()
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d after the first window, want 2", requests.Load())
	}
	clock.Advance(failureCacheInitial + time.Millisecond) // Second window doubled to 10 s.
	query()
	if requests.Load() != 2 {
		t.Fatalf("second failure did not back off: upstream requests = %d", requests.Load())
	}
	clock.Advance(failureCacheInitial)
	query()
	if requests.Load() != 3 {
		t.Fatalf("upstream requests = %d after the doubled window, want 3", requests.Load())
	}

	failed := recorder.GetSnapshot().TopFailedDomains
	if len(failed) != 1 || failed[0].Count != 8 {
		t.Fatalf("failed ranking = %+v", failed)
	}
	reasons := map[string]uint64{}
	for _, r := range failed[0].Reasons {
		reasons[r.Reason] = r.Count
	}
	if reasons[stats.FailureReasonCachedFailure] != 5 || reasons[upstream.Address] != 3 {
		t.Errorf("failure reasons = %v, want 5 cached and 3 upstream failures", reasons)
	}

	// Success clears the backoff: the next failure starts at 5 s again.
	clock.Advance(failureCacheMaximum)
	fail.Store(false)
	if rcode := query(); rcode != dns.RcodeSuccess {
		t.Fatalf("recovered rcode = %d", rcode)
	}
	fail.Store(true)
	h.HandleDnsMsg(testQuery("broken.example."), "", "") // Not cached (no built-in cache): fails again.
	clock.Advance(failureCacheInitial + time.Millisecond)
	before := requests.Load()
	query()
	if requests.Load() != before+1 {
		t.Errorf("backoff not reset after success: upstream requests %d -> %d", before, requests.Load())
	}
}

func TestBackoffIsCappedAtMaximum(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	upstream, requests := failingUpstream(t, &fail)
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	clock := newSteppingClock()
	h.clock = clock.Now
	for range 10 { // 5, 10, 20, 40, 60, 60 ... seconds
		h.HandleDnsMsg(testQuery("broken.example."), "", "")
		clock.Advance(failureCacheMaximum + time.Millisecond)
	}
	if requests.Load() != 10 {
		t.Errorf("upstream requests = %d, want one per capped window", requests.Load())
	}
}

func TestCachedFailureStillServesStaleAnswers(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	upstream, requests := failingUpstream(t, &fail)
	h := NewHandler(true, []*model.Upstream{upstream}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	q := testQuery("stale.example.")
	if err := h.builtInCache.Set(namespacedDNSCacheKey(h.cacheNamespace, q), &cache.CachedMsg{Msg: addressAnswer("192.0.2.1")(q), Expires: time.Now().Add(-time.Minute)}, time.Hour); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		resp := h.HandleDnsMsg(testQuery("stale.example."), "", "")
		if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 || resp.Answer[0].Header().Ttl != 12 {
			t.Fatalf("stale fallback = %+v", resp)
		}
	}
	if requests.Load() != 1 {
		t.Errorf("upstream requests = %d, want 1", requests.Load())
	}
}

func TestOverloadFailuresAreNotCached(t *testing.T) {
	var fail atomic.Bool
	upstream, requests := failingUpstream(t, &fail)
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	if err := h.SetMaxConcurrentQueries(1); err != nil {
		t.Fatal(err)
	}
	h.pendingQueries <- struct{}{}
	if rcode := h.HandleDnsMsg(testQuery("busy.example."), "", "").Rcode; rcode != dns.RcodeServerFailure {
		t.Fatalf("overloaded rcode = %d", rcode)
	}
	<-h.pendingQueries
	if rcode := h.HandleDnsMsg(testQuery("busy.example."), "", "").Rcode; rcode != dns.RcodeSuccess || requests.Load() != 1 {
		t.Errorf("overload was cached as a failure: rcode=%d upstream=%d", rcode, requests.Load())
	}
}

func TestFailureCacheIsBounded(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	upstream, _ := failingUpstream(t, &fail)
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	for i := range failureCacheCapacity + 50 {
		h.HandleDnsMsg(testQuery("broken-"+strconv.Itoa(i)+".example."), "", "")
	}
	h.failuresMu.Lock()
	size := len(h.failures)
	h.failuresMu.Unlock()
	if size > failureCacheCapacity {
		t.Errorf("failure cache holds %d entries, want at most %d", size, failureCacheCapacity)
	}
}

func TestSharedFailureExtendsBackoffOnce(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	upstream, _ := failingUpstream(t, &fail)
	h := NewHandler(false, []*model.Upstream{upstream}, "", logger.New(false), nil)
	clock := newSteppingClock()
	h.clock = clock.Now
	key := namespacedDNSCacheKey(h.cacheNamespace, testQuery("broken.example."))
	for range 32 { // Waiters of one coalesced exchange each report the failure.
		h.rememberFailure(key)
	}
	h.failuresMu.Lock()
	got := h.failures[key].backoff
	h.failuresMu.Unlock()
	if got != failureCacheInitial {
		t.Errorf("backoff after one shared failure = %v, want %v", got, failureCacheInitial)
	}
}
