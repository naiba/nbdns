# v2.0.2 — DNS request-path and cache-memory optimizations

No configuration migration is required from v2.0.0 or v2.0.1. Existing installations with automatic updates may install this release and restart. v1 configurations remain incompatible with v2.

## Changes

- Reduce single-upstream request overhead, cache-key allocations and private response-slice allocations while preserving ECS, reply validation and racing isolation.
- Recover from closed pooled TCP/DoT sessions by retrying eligible DNS queries once on a fresh connection. Do not retry UPDATE, timeout or protocol errors.
- Match concurrent connection establishment to the configured active-connection capacity instead of a hidden limit of 20. The default active capacity remains unchanged.
- Keep Badger compaction tables from growing geometrically and explicitly use two memtables, reducing compaction-buffer retention. These are component budgets, not a process RSS limit.
- Add a reproducible local end-to-end performance harness and regression tests.

## Validation and limitations

The final local matrix covered 19 scenarios, concurrency 1/16/64, three repetitions: 171 runs and 13,309,508 requests with zero response-validation errors. Coverage includes cache hits/writes, custom records, filtering, UDP/TCP, certificate-verified DoT/HTTPS, three-upstream racing, early failures, dropped-packet timeouts and UDP truncation fallback.

At concurrency 16, observed QPS changes versus v2.0.1 were +5.8% cached UDP, +8.4% cached TCP, +10.3% uncached UDP and +6.7% persistent-cache writes. Three-upstream racing improved only about 1%; it is not a major speedup. These are sequential synthetic loopback measurements, not router or internet capacity guarantees.

In a five-minute unique-name write comparison, forced-GC live heap was about 44 MiB versus 83 MiB on the baseline. RSS still grows with an expanding persistent working set and file mappings. A fixed 100,000-domain workload remained approximately stable at 60–61 MiB RSS after warmup.

Earlier short-cooldown matrices encountered host UDP-send EPERM failures and are excluded from performance comparisons. The final matrix used a ten-second interval between cases; independent five-minute continuous workloads also passed. No host networking policy was changed to hide failures.

See [the detailed local performance report](performance-local.md) for measurements, dispersion, methodology and scope. Multi-day, internet and proxy performance are not established by these tests.
