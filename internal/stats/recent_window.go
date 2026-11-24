package stats

import (
	"sync"
	"sync/atomic"
	"time"
)

// recentWindowHours is the dashboard's primary time window. Cumulative
// counters alone hide current problems: an upstream that fails today is
// invisible next to months of history, and removed upstreams keep stale rates.
const recentWindowHours = 24

// latencyBoundsMs are histogram upper bounds; one more bin collects slower replies.
var latencyBoundsMs = [...]float64{5, 10, 20, 30, 50, 75, 100, 150, 200, 300, 500, 750, 1000, 1500, 2000, 3000, 5000}

const latencyBinCount = len(latencyBoundsMs) + 1

// LatencyOverflow marks a percentile above the largest histogram bound.
const LatencyOverflow = -1

func unixHour(t time.Time) int64 { return t.Unix() / 3600 }

// queryHour counts one wall-clock hour. Counters are atomics so the query path
// never takes a lock; only the first record of a new hour rotates the slot.
type queryHour struct {
	hour        atomic.Int64
	queries     atomic.Uint64
	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64
	failed      atomic.Uint64
	blocked     atomic.Uint64
}

type queryWindow struct {
	rotate sync.Mutex
	hours  [recentWindowHours]queryHour
}

func (w *queryWindow) at(now time.Time) *queryHour {
	hour := unixHour(now)
	slot := &w.hours[hour%recentWindowHours]
	if slot.hour.Load() != hour {
		w.rotate.Lock()
		if slot.hour.Load() != hour {
			slot.queries.Store(0)
			slot.cacheHits.Store(0)
			slot.cacheMisses.Store(0)
			slot.failed.Store(0)
			slot.blocked.Store(0)
			slot.hour.Store(hour)
		}
		w.rotate.Unlock()
	}
	return slot
}

func (w *queryWindow) reset() {
	w.rotate.Lock()
	defer w.rotate.Unlock()
	for i := range w.hours {
		w.hours[i].hour.Store(0)
	}
}

// RecentHourJSON is one hour of the trend chart.
type RecentHourJSON struct {
	Start       time.Time `json:"start"`
	Queries     uint64    `json:"queries"`
	CacheHits   uint64    `json:"cache_hits"`
	CacheMisses uint64    `json:"cache_misses"`
	Failed      uint64    `json:"failed"`
	Blocked     uint64    `json:"blocked"`
}

// RecentStats summarizes the last recentWindowHours hours, oldest hour first.
type RecentStats struct {
	WindowHours int              `json:"window_hours"`
	Hours       []RecentHourJSON `json:"hours"`
	Queries     uint64           `json:"queries"`
	CacheHits   uint64           `json:"cache_hits"`
	CacheMisses uint64           `json:"cache_misses"`
	Failed      uint64           `json:"failed"`
	Blocked     uint64           `json:"blocked"`
	HitRate     float64          `json:"hit_rate"`
}

func (w *queryWindow) snapshot(now time.Time) RecentStats {
	current := unixHour(now)
	result := RecentStats{WindowHours: recentWindowHours, Hours: make([]RecentHourJSON, 0, recentWindowHours)}
	for hour := current - recentWindowHours + 1; hour <= current; hour++ {
		entry := RecentHourJSON{Start: time.Unix(hour*3600, 0).UTC()}
		slot := &w.hours[hour%recentWindowHours]
		if slot.hour.Load() == hour {
			entry.Queries, entry.CacheHits, entry.CacheMisses = slot.queries.Load(), slot.cacheHits.Load(), slot.cacheMisses.Load()
			entry.Failed, entry.Blocked = slot.failed.Load(), slot.blocked.Load()
		}
		result.Hours = append(result.Hours, entry)
		result.Queries += entry.Queries
		result.CacheHits += entry.CacheHits
		result.CacheMisses += entry.CacheMisses
		result.Failed += entry.Failed
		result.Blocked += entry.Blocked
	}
	if result.Queries > 0 {
		result.HitRate = float64(result.CacheHits) / float64(result.Queries) * 100
	}
	return result
}

// PersistentQueryHour keeps the recent window across restarts.
type PersistentQueryHour struct {
	Hour        int64  `json:"hour"`
	Queries     uint64 `json:"queries"`
	CacheHits   uint64 `json:"cache_hits"`
	CacheMisses uint64 `json:"cache_misses"`
	Failed      uint64 `json:"failed"`
	Blocked     uint64 `json:"blocked"`
}

func (w *queryWindow) persistent(now time.Time) []PersistentQueryHour {
	current := unixHour(now)
	var result []PersistentQueryHour
	for i := range w.hours {
		slot := &w.hours[i]
		hour := slot.hour.Load()
		if hour <= current-recentWindowHours || hour > current {
			continue
		}
		result = append(result, PersistentQueryHour{Hour: hour, Queries: slot.queries.Load(), CacheHits: slot.cacheHits.Load(),
			CacheMisses: slot.cacheMisses.Load(), Failed: slot.failed.Load(), Blocked: slot.blocked.Load()})
	}
	return result
}

func (w *queryWindow) restore(hours []PersistentQueryHour, now time.Time) {
	current := unixHour(now)
	for _, saved := range hours {
		if saved.Hour <= current-recentWindowHours || saved.Hour > current {
			continue
		}
		slot := w.at(time.Unix(saved.Hour*3600, 0))
		slot.queries.Store(saved.Queries)
		slot.cacheHits.Store(saved.CacheHits)
		slot.cacheMisses.Store(saved.CacheMisses)
		slot.failed.Store(saved.Failed)
		slot.blocked.Store(saved.Blocked)
	}
}

type upstreamHour struct {
	hour             atomic.Int64
	queries          atomic.Uint64
	errors           atomic.Uint64
	latencySamples   atomic.Uint64
	latencySumMicros atomic.Uint64
	latencyBins      [latencyBinCount]atomic.Uint64
}

type upstreamWindow struct {
	rotate sync.Mutex
	hours  [recentWindowHours]upstreamHour
}

func (w *upstreamWindow) at(now time.Time) *upstreamHour {
	hour := unixHour(now)
	slot := &w.hours[hour%recentWindowHours]
	if slot.hour.Load() != hour {
		w.rotate.Lock()
		if slot.hour.Load() != hour {
			slot.queries.Store(0)
			slot.errors.Store(0)
			slot.latencySamples.Store(0)
			slot.latencySumMicros.Store(0)
			for i := range slot.latencyBins {
				slot.latencyBins[i].Store(0)
			}
			slot.hour.Store(hour)
		}
		w.rotate.Unlock()
	}
	return slot
}

func latencyBin(latency time.Duration) int {
	ms := float64(latency) / float64(time.Millisecond)
	for i, bound := range latencyBoundsMs {
		if ms <= bound {
			return i
		}
	}
	return len(latencyBoundsMs)
}

func (w *upstreamWindow) record(now time.Time, isError bool, latency time.Duration) {
	slot := w.at(now)
	slot.queries.Add(1)
	if isError {
		slot.errors.Add(1)
	}
	if latency > 0 {
		slot.latencySamples.Add(1)
		slot.latencySumMicros.Add(uint64(latency / time.Microsecond))
		slot.latencyBins[latencyBin(latency)].Add(1)
	}
}

type upstreamRecent struct {
	queries, errors, samples, sumMicros uint64
	bins                                [latencyBinCount]uint64
}

func (w *upstreamWindow) sum(now time.Time) upstreamRecent {
	current := unixHour(now)
	var total upstreamRecent
	for i := range w.hours {
		slot := &w.hours[i]
		if hour := slot.hour.Load(); hour <= current-recentWindowHours || hour > current {
			continue
		}
		total.queries += slot.queries.Load()
		total.errors += slot.errors.Load()
		total.samples += slot.latencySamples.Load()
		total.sumMicros += slot.latencySumMicros.Load()
		for bin := range slot.latencyBins {
			total.bins[bin] += slot.latencyBins[bin].Load()
		}
	}
	return total
}

// percentileMs interpolates inside the histogram bin holding the percentile;
// LatencyOverflow means it lies above the largest bound.
func (r upstreamRecent) percentileMs(p float64) float64 {
	if r.samples == 0 {
		return 0
	}
	target := p * float64(r.samples)
	var seen float64
	for bin, count := range r.bins {
		if count == 0 {
			continue
		}
		if seen+float64(count) >= target {
			if bin == len(latencyBoundsMs) {
				return LatencyOverflow
			}
			lower := 0.0
			if bin > 0 {
				lower = latencyBoundsMs[bin-1]
			}
			return lower + (target-seen)/float64(count)*(latencyBoundsMs[bin]-lower)
		}
		seen += float64(count)
	}
	return LatencyOverflow
}

// PersistentUpstreamHour keeps an upstream's recent window across restarts.
type PersistentUpstreamHour struct {
	Hour             int64    `json:"hour"`
	Queries          uint64   `json:"queries"`
	Errors           uint64   `json:"errors"`
	LatencySamples   uint64   `json:"latency_samples,omitempty"`
	LatencySumMicros uint64   `json:"latency_sum_us,omitempty"`
	LatencyBins      []uint64 `json:"latency_bins,omitempty"`
}

func (w *upstreamWindow) persistent(now time.Time) []PersistentUpstreamHour {
	current := unixHour(now)
	var result []PersistentUpstreamHour
	for i := range w.hours {
		slot := &w.hours[i]
		hour := slot.hour.Load()
		if hour <= current-recentWindowHours || hour > current {
			continue
		}
		saved := PersistentUpstreamHour{Hour: hour, Queries: slot.queries.Load(), Errors: slot.errors.Load(),
			LatencySamples: slot.latencySamples.Load(), LatencySumMicros: slot.latencySumMicros.Load()}
		if saved.LatencySamples > 0 {
			saved.LatencyBins = make([]uint64, latencyBinCount)
			for bin := range slot.latencyBins {
				saved.LatencyBins[bin] = slot.latencyBins[bin].Load()
			}
		}
		result = append(result, saved)
	}
	return result
}

func (w *upstreamWindow) restore(hours []PersistentUpstreamHour, now time.Time) {
	current := unixHour(now)
	for _, saved := range hours {
		if saved.Hour <= current-recentWindowHours || saved.Hour > current {
			continue
		}
		slot := w.at(time.Unix(saved.Hour*3600, 0))
		slot.queries.Store(saved.Queries)
		slot.errors.Store(saved.Errors)
		slot.latencySamples.Store(saved.LatencySamples)
		slot.latencySumMicros.Store(saved.LatencySumMicros)
		// A file from a build with other bounds would misplace samples; drop them.
		if len(saved.LatencyBins) == latencyBinCount {
			for bin, count := range saved.LatencyBins {
				slot.latencyBins[bin].Store(count)
			}
		} else {
			slot.latencySamples.Store(0)
			slot.latencySumMicros.Store(0)
		}
	}
}
