# Local end-to-end DNS performance harness

Linux-only metrics (`/proc`, `getconf CLK_TCK`). Builds and starts a real NbDNS process per workload, with loopback upstreams and isolated data directories. No router or public resolver is used. Build the server without version ldflags to disable automatic updates. Use a fresh output directory; rerunning an existing directory would reuse its persistent cache.

```sh
go build -o /root/nbdns-performance/nbdns .
go build -o /root/nbdns-performance/dnsperf ./cmd/dnsperf
GOMAXPROCS=2 /root/nbdns-performance/dnsperf \
  -binary /root/nbdns-performance/nbdns \
  -output /root/nbdns-performance/measurements \
  -duration 10s -concurrency 1,16,64 -repeat 3 > measurements.jsonl
```

The target uses GOMAXPROCS=2. The generator and simulated upstreams share another Go process and the same host/cgroup CPU quota; results are host-local comparisons, **not standalone server capacity estimates**. Every run loads 50,000 synthetic filter rules, uses EDNS/DO and administrator ECS, and enables statistics. Connections are reused; TCP reconnect overhead after server query limits is included.

Scenarios: custom records, filter blocks, positive UDP/TCP/DoH cache hits, UDP/TCP/HTTP DoH upstream misses, unique-name persistent-cache writes and hot-cache eviction, successful three-upstream races, races with an early REFUSED, all-upstream failures, negative caching, UDP truncation with TCP fallback. HTTP DoH isolates protocol/handler costs; separate local TLS scenarios are included, but SOCKS costs are not represented.

Every reply is checked for DNS ID, response bit, exact question, expected RCODE and answer address. Any unexpected result rejects the run rather than reporting false success. Upstream request counts are emitted to verify cache/override behavior. Warm-up and server startup are excluded from timing. CPU seconds come from target `/proc/PID/stat` deltas, not the generator and not startup. RSS is sampled every 20 ms during load. HeapBytes is pre-forced-GC live allocation, not retained heap; use the forced-GC heap profile for retention. Allocation counters are process-wide deltas and include small diagnostics overhead, not just the request function.

Artifacts include target logs, stats, forced-GC heap and cumulative allocation profiles. For CPU profiles run a **separate** pass with `-profile -duration 10s`; do not mix those QPS numbers with non-profiled results. Alloc profiles include startup; inspect both before/after footer counters or use per-request allocation deltas for comparisons.

This is closed-loop throughput testing: concurrency N waits for responses before sending more. It measures saturation and latency under that model, not open-loop arrival latency or coordinated-omission-corrected percentiles. Dropped-packet timeouts, local TLS, mixed traffic and five-minute memory observations are included. Real internet/proxy overhead, multi-day stability and cold OS disk caches require separate experiments.

The expanded transport suite uses `max_active_connections=128` and `max_idle_connections=64` so 64 concurrent TCP queries do not deliberately exceed the default 50-connection cap. A separate default-capacity run did return SERVFAIL under overload; those failures are not counted as successful throughput. The production dial concurrency now follows the configured active capacity instead of a hidden fixed limit of 20. Listening ports are probed on both UDP/TCP below the ephemeral source-port range to avoid collisions during high-churn workloads.

Additional workloads include certificate-verified TLS DoT and HTTPS DoH (test CA is trusted only by the child through SSL_CERT_FILE), dropped upstream packets (expected SERVFAIL after the configured timeout), and deterministic mixed traffic (60% hot cache, 20% unique cache writes, 10% custom records, 10% filtered names). A discarded truncated reply or wrong answer owner/class fails validation. `-target-debug` is diagnostic-only: its measurements must not be compared with ordinary runs.

For sustained memory observations use `-duration 300s -scenario cache_write_udp -concurrency 16`, then repeat for `mixed_udp`, against both binaries sequentially. `rss.csv` contains the sampled timeline. This is five-minute observation, not a production longevity guarantee. Unique writes grow the persistent working set by design; RSS includes mapped SST/value-log pages and cannot alone diagnose a leak.

Use `-cooldown 3s` to leave an idle interval between cases. Failed runs print a bounded sample of invalid replies to stderr and exit nonzero; preserve the logs for diagnosis rather than silently discarding individual errors.

`working_set_udp` cycles through 100,000 names. It exercises persistent-cache reads beyond the hot-cache entry limit. `smaps-rollup.txt` records anonymous/file-backed memory at the end of each Linux workload. Final full-matrix acceptance used `-duration 3s -repeat 3 -cooldown 10s`: 19 scenarios × three concurrency levels, 171 runs, zero invalid responses. Earlier shorter-cooldown matrices intermittently returned SERVFAIL (error-only overlay diagnostics identified UDP send EPERM / operation not permitted before the upstream received the packet); their records are retained and excluded, not relabeled as successes. Cooling is an environment constraint, not proof that continuous overload cannot fail.
