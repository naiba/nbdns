package stats

import (
	"math"
	"testing"
	"time"
)

type manualClock struct{ now time.Time }

func (c *manualClock) Now() time.Time { return c.now }

func statsWithClock(start time.Time) (*Stats, *manualClock) {
	clock := &manualClock{now: start}
	s := NewStats()
	s.now = clock.Now
	return s, clock
}

func TestRecentWindowCountsHoursAndDropsExpiredOnes(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 15, 0, 0, time.UTC)
	s, clock := statsWithClock(start)
	s.RecordQuery()
	s.RecordCacheHit()
	s.RecordBlockedQuery("192.0.2.1", "ads.example.")

	clock.now = start.Add(2 * time.Hour)
	for range 3 {
		s.RecordQuery()
	}
	s.RecordCacheMiss()
	s.RecordFailedQuery("192.0.2.1", "broken.example.", "udp://192.0.2.53:53")

	recent := s.GetSnapshot().Recent
	if recent.WindowHours != 24 || len(recent.Hours) != 24 {
		t.Fatalf("window = %d hours, %d entries", recent.WindowHours, len(recent.Hours))
	}
	if recent.Queries != 4 || recent.CacheHits != 1 || recent.CacheMisses != 1 || recent.Failed != 1 || recent.Blocked != 1 || recent.HitRate != 25 {
		t.Errorf("recent totals = %+v", recent)
	}
	last, twoHoursAgo := recent.Hours[23], recent.Hours[21]
	if !last.Start.Equal(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)) || last.Queries != 3 || last.Failed != 1 {
		t.Errorf("current hour = %+v", last)
	}
	if twoHoursAgo.Queries != 1 || twoHoursAgo.Blocked != 1 || recent.Hours[22].Queries != 0 {
		t.Errorf("older hours = %+v / %+v", twoHoursAgo, recent.Hours[22])
	}

	// 24 hours after the first record its hour leaves the window; the slot is reused.
	clock.now = start.Add(24 * time.Hour)
	s.RecordQuery()
	recent = s.GetSnapshot().Recent
	if recent.Queries != 4 || recent.Blocked != 0 || recent.Hours[23].Queries != 1 {
		t.Errorf("window after a day = %+v", recent)
	}
	// Cumulative counters are unaffected by the window.
	if q := s.GetSnapshot().Queries; q.Total != 5 || q.Blocked != 1 || q.Failed != 1 {
		t.Errorf("cumulative = %+v", q)
	}
}

func TestRecentWindowAndFailuresPersistAndReset(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s, _ := statsWithClock(start)
	s.RecordQuery()
	s.RecordBlockedQuery("192.0.2.1", "ads.example.")
	s.RecordFailedQuery("192.0.2.1", "broken.example.", "udp://192.0.2.53:53")
	s.RecordFailedQuery("192.0.2.2", "broken.example.", FailureReasonOverload)
	s.RecordFailedQuery("192.0.2.2", "broken.example.", FailureReasonOverload)
	s.RecordUpstreamQuery("udp://192.0.2.53:53", false, 40*time.Millisecond)
	dir := t.TempDir()
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}

	loaded, clock := statsWithClock(start.Add(30 * time.Minute))
	if err := loaded.Load(dir); err != nil {
		t.Fatal(err)
	}
	snapshot := loaded.GetSnapshot()
	if snapshot.Recent.Queries != 1 || snapshot.Recent.Blocked != 1 || snapshot.Recent.Failed != 3 || snapshot.Queries.Blocked != 1 {
		t.Errorf("restored recent = %+v, cumulative = %+v", snapshot.Recent, snapshot.Queries)
	}
	if len(snapshot.Upstreams) != 1 || snapshot.Upstreams[0].RecentQueries != 1 || snapshot.Upstreams[0].AvgLatencyMs != 40 {
		t.Errorf("restored upstream = %+v", snapshot.Upstreams)
	}
	failed := snapshot.TopFailedDomains
	if len(failed) != 1 || failed[0].Key != "broken.example." || failed[0].Count != 3 || failed[0].TopClient != "192.0.2.2" {
		t.Fatalf("restored failed ranking = %+v", failed)
	}
	if len(failed[0].Reasons) != 2 || failed[0].Reasons[0] != (ReasonCountJSON{FailureReasonOverload, 2}) ||
		failed[0].Reasons[1] != (ReasonCountJSON{"udp://192.0.2.53:53", 1}) {
		t.Errorf("failure reasons = %+v", failed[0].Reasons)
	}

	// Hours older than the window are not restored.
	expired, _ := statsWithClock(start.Add(25 * time.Hour))
	if err := expired.Load(dir); err != nil {
		t.Fatal(err)
	}
	if got := expired.GetSnapshot(); got.Recent.Queries != 0 || got.Upstreams[0].RecentQueries != 0 || got.Queries.Total != 1 {
		t.Errorf("expired window restored: %+v / %+v", got.Recent, got.Upstreams)
	}

	clock.now = start.Add(time.Hour)
	loaded.Reset()
	snapshot = loaded.GetSnapshot()
	if snapshot.Recent.Queries != 0 || snapshot.Queries.Blocked != 0 || len(snapshot.TopFailedDomains) != 0 || len(snapshot.Upstreams) != 0 {
		t.Errorf("after reset = %+v", snapshot)
	}
}

func TestUpstreamRecentErrorRateLatencyAndLocalTimestamp(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s, clock := statsWithClock(start)
	const address = "tcp-tls://dns.example:853"
	// An old failure burst outside the window must not color today's error rate.
	for range 10 {
		s.RecordUpstreamQuery(address, true, 0)
	}
	clock.now = start.Add(30 * time.Hour)
	for i := 1; i <= 100; i++ {
		latency := 8 * time.Millisecond // bin (5, 10]
		if i > 90 {
			latency = 400 * time.Millisecond // bin (300, 500]
		}
		s.RecordUpstreamQuery(address, false, latency)
	}
	s.RecordUpstreamQuery(address, true, 0) // Failures carry no latency sample.

	got := s.GetSnapshot().Upstreams[0]
	if got.TotalQueries != 111 || got.Errors != 11 || got.RecentQueries != 101 || got.RecentErrors != 1 {
		t.Fatalf("counts = %+v", got)
	}
	if math.Abs(got.RecentErrorRate-100.0/101) > 1e-9 {
		t.Errorf("recent error rate = %v", got.RecentErrorRate)
	}
	if math.Abs(got.AvgLatencyMs-(90*8+10*400)/100.0) > 1e-9 {
		t.Errorf("average latency = %v", got.AvgLatencyMs)
	}
	// The 95th sample lies halfway through the 10 samples in (300, 500].
	if math.Abs(got.P95LatencyMs-400) > 1e-9 {
		t.Errorf("p95 latency = %v, want 400", got.P95LatencyMs)
	}
	if got.LastUsed != "2026-05-02T16:00:00Z" {
		t.Errorf("last used = %q, want RFC 3339 UTC", got.LastUsed)
	}

	s.RecordUpstreamQuery("udp://never.example:53", false, 10*time.Second)
	for _, upstream := range s.GetSnapshot().Upstreams {
		if upstream.Address == "udp://never.example:53" && upstream.P95LatencyMs != LatencyOverflow {
			t.Errorf("overflow p95 = %v", upstream.P95LatencyMs)
		}
	}
}

func TestRetainUpstreamsDropsRemovedUpstreams(t *testing.T) {
	s := NewStats()
	s.RecordUpstreamQuery("udp://kept.example:53", false, time.Millisecond)
	s.RecordUpstreamQuery("udp://removed.example:53", true, 0)
	s.RetainUpstreams([]string{"udp://kept.example:53", "udp://unused.example:53"})
	got := s.GetSnapshot().Upstreams
	if len(got) != 1 || got[0].Address != "udp://kept.example:53" {
		t.Errorf("upstreams = %+v", got)
	}
}
