# v2.4.0 — Prefetch, one-day cache cap and an accurate hit rate

Frequently queried names now stay cached: entries are refreshed shortly before they expire, and the cache keeps long-TTL answers for up to a day instead of an hour. The dashboard's cache hit rate now measures only queries that consult the cache. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- **Prefetch.** A cache hit in the last tenth of an entry's lifetime refreshes it in the background, as Unbound and BIND do; entries living under 10 seconds are not prefetched. The client is answered from the cache immediately, each entry is refreshed at most once at a time, and refreshes are skipped while `max_concurrent_queries` is reached so they never displace client queries. Expired answers are still never served while upstreams respond, so short-TTL records such as DDNS names change on time. Shutdown waits for running refreshes. The dashboard and `/api/stats` report prefetches for the last 24 hours and in total.
- **`cache_max_ttl`.** The cap on cached lifetimes was a fixed hour, so names with longer TTLs were fetched again every hour. It is now configurable in seconds and defaults to one day, like Unbound's `cache-max-ttl`; shorter DNS TTLs are honored as before. Set `"cache_max_ttl": 3600` to keep the previous behavior. Fewer refetches also mean fewer cache writes to flash storage.

## Fixes

- The cache hit rate divided cache hits by all queries, including blocked queries and custom records that never consult the cache, which understated it by the share of blocked queries. Both the lifetime and the 24-hour rate now use hits ÷ (hits + misses).
- An expired cache entry was counted as a hit although the query went upstream; it now counts as a miss. Lifetime totals recorded before this release keep the old counting.

## Validation

Regression tests reproduce both counting errors, check the default and configured lifetime caps against upstream TTLs and the configuration and schema validation, and cover prefetch: refreshing in the last tenth of the lifetime, skipping fresh and short-lived entries, one refresh per entry under concurrent hits, yielding to client admission, Close waiting for a running refresh and refusing new ones afterwards, and persisting the prefetch counters. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets.
