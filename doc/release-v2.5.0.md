# v2.5.0 — Caching resolution failures (RFC 9520)

No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- When every upstream fails for a query, nbdns now caches the failure as RFC 9520 recommends: for 5 seconds, doubling with each consecutive failure of the same query up to 60 seconds, and cleared by the next successful resolution. Clients retrying within the window receive SERVFAIL, or a stale cached answer when one exists, without new upstream queries. Previously every retry went upstream again, so a client persistently retrying a name its upstream could not resolve repeated the same upstream query, and such bursts could exhaust `max_concurrent_queries` and fail unrelated queries.
- Waiters of one coalesced upstream exchange share a single failure and extend the backoff once.
- Rejections by `max_concurrent_queries` and names without an upstream are local conditions and are not cached. Bootstrap lookups of upstream hostnames are never failure-cached, so upstream connections recover immediately.
- The failed-domain ranking shows queries answered from the failure cache as a separate reason.
- The failure cache is in memory, bounded to 4,096 queries, and does not survive a restart.

## Validation

Tests cover caching and the doubling backoff with an injected clock, the 60-second cap, reset after success, stale answers served during a cached failure, uncached overload rejections, the bounded size, a coalesced failure extending the backoff once, and uncached bootstrap failures. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets.
