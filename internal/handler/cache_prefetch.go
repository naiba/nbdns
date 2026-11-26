package handler

import (
	"time"

	"github.com/miekg/dns"

	"github.com/naiba/nbdns/internal/cache"
)

// Prefetch (as in Unbound and BIND): a cache hit in the last tenth of an
// entry's lifetime refreshes it in the background, so hot names rarely expire
// and miss. Unlike serving expired answers ("optimistic caching") it never
// answers past the TTL, so short-TTL records such as DDNS names stay exact.
const (
	prefetchWindowDivisor = 10               // Refresh within the last 1/10 of the lifetime
	prefetchMinLifetime   = 10 * time.Second // Shorter lifetimes would refresh on almost every hit
)

// maybePrefetch is called for a fresh cache hit of req.
func (h *Handler) maybePrefetch(req *dns.Msg, cacheKey string, cached *cache.CachedMsg) {
	// The bootstrap handler borrows the owner's cache and is never closed, so it
	// could not wait for a refresh; its rare upstream-hostname lookups skip prefetch.
	if h.borrowedCache {
		return
	}
	lifetime := getDnsResponseTtl(cached.Msg, h.maxCacheTTL)
	if lifetime < prefetchMinLifetime || time.Until(cached.Expires) > lifetime/prefetchWindowDivisor {
		return
	}
	h.backgroundMu.Lock()
	if h.closing || h.prefetching[cacheKey] {
		h.backgroundMu.Unlock()
		return
	}
	// Client queries keep priority: skip the refresh when admission is full;
	// a later hit in the window retries.
	select {
	case h.pendingQueries <- struct{}{}:
	default:
		h.backgroundMu.Unlock()
		return
	}
	h.prefetching[cacheKey] = true
	h.background.Add(1)
	h.backgroundMu.Unlock()

	query := req.Copy()
	go func() {
		defer func() {
			<-h.pendingQueries
			h.backgroundMu.Lock()
			delete(h.prefetching, cacheKey)
			h.backgroundMu.Unlock()
			h.background.Done()
		}()
		resp := h.exchange(query)
		normalizeNegativeTTL(resp)
		if h.cacheResponse(query, cacheKey, resp) && h.stats != nil {
			h.stats.RecordPrefetch()
		}
	}()
}

// normalizeNegativeTTL applies the RFC 2308 SOA minimum to negative answers.
func normalizeNegativeTTL(resp *dns.Msg) {
	if len(resp.Answer) == 0 && (resp.Rcode == dns.RcodeNameError || resp.Rcode == dns.RcodeSuccess) {
		for _, rr := range resp.Ns {
			if soa, ok := rr.(*dns.SOA); ok {
				soa.Hdr.Ttl = min(soa.Hdr.Ttl, soa.Minttl)
			}
		}
	}
}

// cacheResponse stores a cacheable upstream answer and reports whether it did.
// Expired entries stay one more hour as a fallback while upstreams fail.
func (h *Handler) cacheResponse(req *dns.Msg, cacheKey string, resp *dns.Msg) bool {
	if h.builtInCache == nil || !shouldCacheResponse(resp) || !validateResponse(req, resp, h.logger) {
		return false
	}
	ttl := getDnsResponseTtl(resp, h.maxCacheTTL)
	if ttl <= 0 {
		return false
	}
	cached := &cache.CachedMsg{Msg: resp.Copy(), Expires: time.Now().Add(ttl)}
	if err := h.builtInCache.Set(cacheKey, cached, ttl+time.Hour); err != nil {
		h.logger.Printf("Failed to cache response: %v", err)
		return false
	}
	return true
}
