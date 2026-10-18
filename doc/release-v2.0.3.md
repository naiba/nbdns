# v2.0.3 — Bounded DNS concurrency and reusable DoH connections

No configuration migration is required from v2.0.x. The new `max_concurrent_queries` setting defaults to **64** when omitted or set to zero. Existing installations with automatic updates may install this release and restart. v1 configurations remain incompatible with v2.

## Changes

- Coalesce identical concurrent cache misses, including when caching is disabled. Share upstream work and cache writes, with private responses for each client. Preserve query flags, EDNS options and routing distinctions.
- Bound pending cache misses, including singleflight waiters. Excess requests receive SERVFAIL or eligible stale cached data. Cache hits, local records and filtered responses bypass the limit. This is a concurrency limit, not a QPS limit.
- Cancel losing upstream queries after a valid answer wins. Interrupt UDP/TCP reads and discard canceled pooled connections without affecting subsequent users of the pool. Existing TCP/DoT pool dials remain bounded by their connection limits and timeouts.
- Enable HTTP/2 negotiation with custom DoH dialers and retain reusable HTTP/1 connections across bursts. Apply configured active/idle connection limits to DoH upstreams and propagate cancellation through context-aware proxy dialing.
- Coalesce concurrent bootstrap DNS lookups. HTTPS/DoT upstream certificates continue to be verified normally. No inbound HTTPS listener or certificate management is added; inbound TLS remains with the front-end proxy.

## Validation

Regression tests first reproduced HTTP/1 negotiation against an HTTP/2-capable server, 14 connections for two bursts of 8 requests, and slow upstream requests continuing after a winning answer. The fixes negotiate HTTP/2, reuse 8 connections, and cancel the losing request. Further tests cover overload, cache/stale fallback, connection-pool reuse, DNS metadata, failure retries and concurrent response isolation.

Local unique-name HTTPS comparisons measured median QPS of 342 → 2937 at concurrency 16 and 182 → 4311 at concurrency 64, with zero invalid responses. These synthetic HTTP/1 upstream measurements isolate connection reuse; HTTP/2 negotiation is tested separately. UDP results were mixed (-4.7% at concurrency 16, +18.2% at 64), so no uniform UDP throughput gain is claimed.

The performance comparisons used the earlier concurrency limit of 256. The shipping default was subsequently reduced to 64 for router use; the performance matrix was not repeated after that default change. See [the detailed validation report](upstream-concurrency-performance.md) for scope and limitations. Generated local benchmark binaries, databases, certificates and raw profiling directories were cleaned before release; regression-test sources and the reproducible benchmark tool are retained.
