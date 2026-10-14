# v2.0.1 — lower allocation overhead

No configuration changes relative to v2.0.0.

- Build DNS cache keys with stack-backed numeric appends instead of formatted interfaces. Persisted key bytes and DNSSEC/CD/class isolation remain unchanged.
- Allocate filter trie nodes in stable batches and avoid repeated map lookups during insertion. Keep the existing trie lookup algorithm and exception precedence.
- Add cache-key compatibility coverage and cross-load exception/duplicate-rule regression tests.

## Local performance measurements

Linux ARM64, Go 1.27.2 toolchain, three benchmark repetitions. Synthetic inputs, not router load tests; timings are illustrative, not end-to-end DNS latency guarantees.

| Benchmark | Before | After |
|---|---:|---:|
| Mixed-case DNS cache key (median) | 455.6 ns/op | 173.9 ns/op |
| Cache key allocated bytes | 80 B/op | 48 B/op |
| Cache key allocations | 4/op | 2/op |
| Load 50,000 filter rules (median) | 19.85 ms/op | 17.73 ms/op |
| Filter load allocated bytes | 5.55 MB/op | 5.56 MB/op |
| Filter load allocations | 100,280/op | 50,476/op |

Filter matching remains allocation-free; measured hit/miss timings remained approximately 142/122 ns. The node batching optimization reduces object count, not total allocated bytes or a proven amount of process RSS. CPU and heap profiles identified trie construction as the primary filter-load allocator. A full-suffix hash index was rejected because it slowed lookups.

Reproduce:

```sh
go test ./internal/filter -run '^$' -bench 'Benchmark(FilterLoad50k|Blocked50k)' -benchmem -count=3 -cpuprofile filter.cpu -memprofile filter.heap
go test ./internal/handler -run '^$' -bench BenchmarkDNSCacheKey -benchmem -count=3
go tool pprof -top filter.cpu
go tool pprof -top -alloc_space filter.heap
```

Existing installations may automatically install published releases and restart. v1 configurations still require preparation for v2; see the v2.0.0 release notes.
