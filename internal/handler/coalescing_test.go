package handler

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/cache"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

type countedDNSCache struct {
	cache.Cache
	writes atomic.Int32
}

func (c *countedDNSCache) Set(key string, msg *cache.CachedMsg, ttl time.Duration) error {
	c.writes.Add(1)
	return c.Cache.Set(key, msg, ttl)
}

func concurrentDNSQueries(t *testing.T, h *Handler, wantRcode int, dnssec bool) []*dns.Msg {
	t.Helper()
	const clients = 32
	start := make(chan struct{})
	responses := make(chan *dns.Msg, clients)
	var ready sync.WaitGroup
	ready.Add(clients)
	for client := range clients {
		go func() {
			q := testQuery("Shared.Example.")
			q.Id = uint16(client + 1)
			q.SetEdns0(1232, dnssec)
			ready.Done()
			<-start
			resp := h.HandleDnsMsg(q, "192.0.2.1", "")
			if resp.Rcode != wantRcode || resp.Id != q.Id || len(resp.Question) != 1 || resp.Question[0] != q.Question[0] || resp.RecursionDesired != q.RecursionDesired || resp.CheckingDisabled != q.CheckingDisabled || resp.IsEdns0() == nil || resp.IsEdns0().Do() != dnssec || resp.IsEdns0().UDPSize() != 1232 {
				t.Errorf("client %d received incorrect metadata: %v", client, resp)
			}
			responses <- resp
		}()
	}
	ready.Wait()
	close(start)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	result := make([]*dns.Msg, 0, clients)
	for range clients {
		select {
		case resp := <-responses:
			result = append(result, resp)
		case <-deadline.C:
			t.Fatal("concurrent DNS queries did not finish")
		}
	}
	return result
}

func coalescedDNSAnswer(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg).SetReply(req)
	resp.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}}}
	return resp
}

func TestConcurrentDNSQueriesShareUpstreamWork(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, upstreamCount := range []int{1, 2} {
			t.Run(fmt.Sprintf("cache_%t/upstreams_%d", cached, upstreamCount), func(t *testing.T) {
				var upstreams []*model.Upstream
				var calls []*atomic.Int32
				for range upstreamCount {
					up, count := testUpstream(t, func(req *dns.Msg) *dns.Msg {
						// Hold the upstream response so the simultaneous clients overlap.
						time.Sleep(150 * time.Millisecond)
						return coalescedDNSAnswer(req)
					})
					upstreams = append(upstreams, up)
					calls = append(calls, count)
				}
				recorder := stats.NewStats()
				h := NewHandler(cached, upstreams, t.TempDir(), logger.New(false), recorder)
				t.Cleanup(func() { _ = h.Close() })
				var counted *countedDNSCache
				if cached {
					counted = &countedDNSCache{Cache: h.builtInCache}
					h.builtInCache = counted
				}
				responses := concurrentDNSQueries(t, h, dns.RcodeSuccess, true)
				for _, count := range calls {
					if got := count.Load(); got != 1 {
						t.Errorf("upstream received %d duplicate queries, want 1", got)
					}
				}
				if recorder.TotalQueries.Load() != 32 || recorder.FailedQueries.Load() != 0 {
					t.Fatalf("incorrect per-client query statistics: %+v", recorder.GetSnapshot())
				}
				for _, resp := range responses {
					if len(resp.Answer) != 1 || resp.Answer[0].Header().Ttl <= 1 || resp.Answer[0].Header().Ttl > 60 || resp.Question[0].Name == "mutated.example." || resp.IsEdns0().UDPSize() != 1232 {
						t.Fatalf("incorrect answer: %v", resp)
					}
					resp.Answer[0].Header().Ttl = 1
					resp.Question[0].Name = "mutated.example."
					resp.IsEdns0().SetUDPSize(512)
				}
				if cached {
					if got := counted.writes.Load(); got != 1 {
						t.Errorf("cache writes = %d, want 1", got)
					}
					q := testQuery("shared.example.")
					q.SetEdns0(1232, true)
					resp := h.HandleDnsMsg(q, "", "")
					if len(resp.Answer) != 1 || resp.Answer[0].Header().Ttl <= 1 || recorder.CacheHits.Load() != 1 {
						t.Fatalf("shared response mutation damaged cache or cache was bypassed: %v", resp)
					}
					for _, count := range calls {
						if got := count.Load(); got != 1 {
							t.Errorf("cache hit reached upstream: %d", got)
						}
					}
				}
			})
		}
	}
}

func TestConcurrentDNSFailureIsSharedAndCanRetry(t *testing.T) {
	var recovered atomic.Bool
	up, calls := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		time.Sleep(150 * time.Millisecond)
		if recovered.Load() {
			return coalescedDNSAnswer(req)
		}
		return new(dns.Msg).SetRcode(req, dns.RcodeServerFailure)
	})
	recorder := stats.NewStats()
	h := NewHandler(true, []*model.Upstream{up}, t.TempDir(), logger.New(false), recorder)
	t.Cleanup(func() { _ = h.Close() })
	concurrentDNSQueries(t, h, dns.RcodeServerFailure, true)
	if calls.Load() != 1 || recorder.FailedQueries.Load() != 32 {
		t.Fatalf("failure not shared/accounted per client: calls=%d failures=%d", calls.Load(), recorder.FailedQueries.Load())
	}
	recovered.Store(true)
	concurrentDNSQueries(t, h, dns.RcodeSuccess, true)
	if calls.Load() != 2 || recorder.TotalQueries.Load() != 64 || recorder.FailedQueries.Load() != 32 {
		t.Fatalf("failed flight was retained: calls=%d stats=%+v", calls.Load(), recorder.GetSnapshot())
	}
	snapshot := recorder.GetSnapshot()
	if len(snapshot.Upstreams) != 1 || snapshot.Upstreams[0].TotalQueries != 2 || snapshot.Upstreams[0].Errors != 1 {
		t.Fatalf("upstream statistics must count actual exchanges: %+v", snapshot.Upstreams)
	}
}

func TestDNSFlightKeyPreservesQuerySemantics(t *testing.T) {
	query := testQuery("Shared.Example.")
	query.SetEdns0(1232, true)
	before, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	key, err := dnsFlightKey(query)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*dns.Msg)
		same   bool
	}{
		{"transaction_id", func(q *dns.Msg) { q.Id++ }, true},
		{"question_case", func(q *dns.Msg) { q.Question[0].Name = "shared.example." }, false},
		{"compression", func(q *dns.Msg) { q.Compress = true }, true},
		{"domain", func(q *dns.Msg) { q.Question[0].Name = "other.example." }, false},
		{"record_type", func(q *dns.Msg) { q.Question[0].Qtype = dns.TypeAAAA }, false},
		{"record_class", func(q *dns.Msg) { q.Question[0].Qclass = dns.ClassCHAOS }, false},
		{"recursion_desired", func(q *dns.Msg) { q.RecursionDesired = false }, false},
		{"checking_disabled", func(q *dns.Msg) { q.CheckingDisabled = true }, false},
		{"authenticated_data", func(q *dns.Msg) { q.AuthenticatedData = true }, false},
		{"dnssec_ok", func(q *dns.Msg) { q.IsEdns0().SetDo(false) }, false},
		{"edns_size", func(q *dns.Msg) { q.IsEdns0().SetUDPSize(512) }, false},
		{"no_edns", func(q *dns.Msg) { q.Extra = nil }, false},
		{"edns_version", func(q *dns.Msg) { q.IsEdns0().SetVersion(1) }, false},
		{"edns_cookie", func(q *dns.Msg) {
			q.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0123456789abcdef"}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := query.Copy()
			tc.change(changed)
			got, err := dnsFlightKey(changed)
			if err != nil || (got == key) != tc.same {
				t.Fatalf("flight key equality = %t, want %t, error = %v", got == key, tc.same, err)
			}
		})
	}
	after, err := query.Pack()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("key generation mutated caller request: %v", err)
	}
}

func TestDifferentDNSQueriesDoNotWaitForEachOther(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	up, calls := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		if req.Question[0].Qtype == dns.TypeA {
			close(entered)
			<-release
		}
		return new(dns.Msg).SetReply(req)
	})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	h := NewHandler(false, []*model.Upstream{up}, "", logger.New(false), nil)
	first := make(chan *dns.Msg, 1)
	go func() { first <- h.HandleDnsMsg(testQuery("shared.example."), "", "") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first query did not reach upstream")
	}
	query := testQuery("shared.example.")
	query.Question[0].Qtype = dns.TypeAAAA
	second := make(chan *dns.Msg, 1)
	go func() { second <- h.HandleDnsMsg(query, "", "") }()
	select {
	case resp := <-second:
		if resp.Rcode != dns.RcodeSuccess || resp.Id != query.Id || resp.Question[0] != query.Question[0] || calls.Load() != 2 {
			t.Fatalf("different query was merged: %v / calls=%d", resp, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("AAAA query waited for the unrelated A query")
	}
	unblock()
	select {
	case resp := <-first:
		if resp.Rcode != dns.RcodeSuccess || resp.Question[0].Qtype != dns.TypeA {
			t.Fatalf("incorrect first reply: %v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("first query did not finish after release")
	}
}

func TestExpiredDNSCacheCoalescesRefreshAndPreservesFallback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("upstream_failure_%t", fail), func(t *testing.T) {
			up, calls := testUpstream(t, func(req *dns.Msg) *dns.Msg {
				time.Sleep(150 * time.Millisecond)
				if fail {
					return new(dns.Msg).SetRcode(req, dns.RcodeServerFailure)
				}
				return coalescedDNSAnswer(req)
			})
			recorder := stats.NewStats()
			h := NewHandler(true, []*model.Upstream{up}, t.TempDir(), logger.New(false), recorder)
			t.Cleanup(func() { _ = h.Close() })
			query := testQuery("shared.example.")
			query.SetEdns0(1232, false)
			stale := coalescedDNSAnswer(query)
			stale.AuthenticatedData = true
			key := namespacedDNSCacheKey(h.cacheNamespace, query)
			if err := h.builtInCache.Set(key, &cache.CachedMsg{Msg: stale, Expires: time.Now().Add(-time.Minute)}, time.Hour); err != nil {
				t.Fatal(err)
			}
			responses := concurrentDNSQueries(t, h, dns.RcodeSuccess, false)
			for _, resp := range responses {
				if len(resp.Answer) != 1 {
					t.Fatalf("missing refreshed/fallback answer: %v", resp)
				}
				if fail && (resp.Answer[0].Header().Ttl != 12 || resp.AuthenticatedData) {
					t.Fatalf("invalid stale fallback metadata: %v", resp)
				}
				if !fail && resp.Answer[0].Header().Ttl <= 12 {
					t.Fatalf("expired entry was returned instead of refreshed: %v", resp)
				}
			}
			if calls.Load() != 1 || (fail && recorder.FailedQueries.Load() != 32) || (!fail && recorder.FailedQueries.Load() != 0) {
				t.Fatalf("incorrect refresh count/statistics: calls=%d stats=%+v", calls.Load(), recorder.GetSnapshot())
			}
		})
	}
}

type delayedDNSCacheRead struct {
	cache.Cache
	reads    atomic.Int32
	observed chan struct{}
	release  chan struct{}
}

func (c *delayedDNSCacheRead) Get(key string) (*cache.CachedMsg, bool) {
	value, ok := c.Cache.Get(key)
	if c.reads.Add(1) == 1 {
		close(c.observed)
		<-c.release
	}
	return value, ok
}

func TestDNSFlightRechecksCacheAfterInitialMiss(t *testing.T) {
	up, calls := testUpstream(t, coalescedDNSAnswer)
	h := NewHandler(true, []*model.Upstream{up}, t.TempDir(), logger.New(false), nil)
	t.Cleanup(func() { _ = h.Close() })
	delayed := &delayedDNSCacheRead{Cache: h.builtInCache, observed: make(chan struct{}), release: make(chan struct{})}
	h.builtInCache = delayed
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(delayed.release) }) }
	t.Cleanup(unblock)
	query := testQuery("shared.example.")
	response := make(chan *dns.Msg, 1)
	go func() { response <- h.HandleDnsMsg(query, "", "") }()
	select {
	case <-delayed.observed:
	case <-time.After(time.Second):
		t.Fatal("initial cache lookup did not finish")
	}
	key := namespacedDNSCacheKey(h.cacheNamespace, query)
	if err := delayed.Set(key, &cache.CachedMsg{Msg: coalescedDNSAnswer(query), Expires: time.Now().Add(time.Minute)}, time.Minute); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case resp := <-response:
		if resp.Rcode != dns.RcodeSuccess || resp.Id != query.Id || len(resp.Answer) != 1 || calls.Load() != 0 || delayed.reads.Load() != 2 {
			t.Fatalf("cache was not rechecked: response=%v calls=%d reads=%d", resp, calls.Load(), delayed.reads.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("query did not finish after cache lookup release")
	}
}
