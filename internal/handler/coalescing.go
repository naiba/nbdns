package handler

import (
	"fmt"
	"time"

	"github.com/miekg/dns"
)

// SetMaxConcurrentQueries configures admission before the handler starts serving.
func (h *Handler) SetMaxConcurrentQueries(limit int) error {
	if limit <= 0 {
		return fmt.Errorf("max_concurrent_queries must be positive")
	}
	h.pendingQueries = make(chan struct{}, limit)
	return nil
}

// dnsFlightKey ignores transaction IDs and wire compression, but retains all
// query fields. The cache key alone is insufficient: RD and EDNS options can
// change upstream responses. Keep question case because upstream routing rules
// currently match case-sensitively, even though DNS names are case-insensitive.
func dnsFlightKey(req *dns.Msg) (string, error) {
	query := *req
	query.Id = 0
	query.Compress = false
	wire, err := query.Pack()
	return string(wire), err
}

// resolveCacheMiss reports overloaded when admission rejected the query before
// any upstream was asked, so failure statistics do not blame the upstreams.
func (h *Handler) resolveCacheMiss(req *dns.Msg, cacheKey string) (resp *dns.Msg, overloaded bool) {
	// Singleflight only bounds duplicate upstream work, not distinct queries or
	// waiting clients. Reject overload before allocating a flight or queueing;
	// cache hits and local answers have already bypassed this admission gate.
	select {
	case h.pendingQueries <- struct{}{}:
		defer func() { <-h.pendingQueries }()
	default:
		return new(dns.Msg).SetRcode(req, dns.RcodeServerFailure), true
	}
	key, err := dnsFlightKey(req)
	if err != nil {
		return new(dns.Msg).SetRcode(req, dns.RcodeFormatError), false
	}
	// Cache misses must share the entire fetch-and-store operation, including
	// when caching is disabled. Otherwise a cold/expired hot name fans out one
	// request per client per upstream, and writes the same cache entry repeatedly.
	value, _, shared := h.upstreamQueries.Do(key, func() (any, error) {
		if h.builtInCache != nil {
			// Another flight may have populated the cache since our initial miss.
			if cached, ok := h.builtInCache.Get(cacheKey); ok && cached.Expires.After(time.Now()) {
				return replyUpdateTtl(req, cached.Msg.Copy(), uint32(time.Until(cached.Expires).Seconds())), nil
			}
		}
		resp := h.exchange(req)
		normalizeNegativeTTL(resp)
		h.cacheResponse(req, cacheKey, resp)
		return resp, nil
	})
	resp = value.(*dns.Msg)
	if shared {
		// Every waiter, including the leader, needs private records before the
		// caller restores its ID/question/EDNS or truncates a UDP response.
		resp = resp.Copy()
	}
	return resp, false
}
