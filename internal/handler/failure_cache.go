package handler

import (
	"time"
)

// RFC 9520 (negative caching of DNS resolution failures): when every upstream
// fails, remember the failure briefly instead of resending each client retry.
// Without it every client retry of a failing name went upstream again, and
// retry bursts could exhaust max_concurrent_queries.
// Consecutive failures of the same query double the window; success resets it.
const (
	failureCacheInitial  = 5 * time.Second
	failureCacheMaximum  = time.Minute
	failureCacheCapacity = 4096 // Bounds memory against floods of distinct failing names
)

type cachedFailure struct {
	until   time.Time     // Answer SERVFAIL without asking upstreams until then
	backoff time.Duration // Length of the current window, doubled on the next failure
}

func (h *Handler) now() time.Time {
	if h.clock != nil {
		return h.clock()
	}
	return time.Now()
}

// failureCached reports whether key failed recently and must not be retried yet.
func (h *Handler) failureCached(key string) bool {
	if h.failures == nil { // Disabled (bootstrap handler)
		return false
	}
	h.failuresMu.Lock()
	defer h.failuresMu.Unlock()
	failure, ok := h.failures[key]
	return ok && h.now().Before(failure.until)
}

// rememberFailure starts or extends the failure window of key.
func (h *Handler) rememberFailure(key string) {
	if h.failures == nil {
		return
	}
	now := h.now()
	h.failuresMu.Lock()
	defer h.failuresMu.Unlock()
	backoff := failureCacheInitial
	previous, ok := h.failures[key]
	if ok && now.Before(previous.until) {
		// Concurrent queries sharing one upstream failure report it once each;
		// counting every report would jump straight to the maximum backoff.
		return
	}
	if ok && now.Sub(previous.until) <= failureCacheMaximum {
		// Still failing soon after the last window: back off further.
		backoff = min(previous.backoff*2, failureCacheMaximum)
	} else if len(h.failures) >= failureCacheCapacity {
		h.evictFailuresLocked(now)
	}
	h.failures[key] = cachedFailure{until: now.Add(backoff), backoff: backoff}
}

// forgetFailure resets the backoff of key after a successful resolution.
func (h *Handler) forgetFailure(key string) {
	h.failuresMu.Lock()
	if len(h.failures) > 0 {
		delete(h.failures, key)
	}
	h.failuresMu.Unlock()
}

// evictFailuresLocked drops failures whose backoff no longer matters (their
// window ended at least failureCacheMaximum ago), or every entry if the cache is
// still full of current failures.
func (h *Handler) evictFailuresLocked(now time.Time) {
	for key, failure := range h.failures {
		if now.Sub(failure.until) > failureCacheMaximum {
			delete(h.failures, key)
		}
	}
	if len(h.failures) >= failureCacheCapacity {
		clear(h.failures)
	}
}
