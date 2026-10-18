# v2.0.4 — Shared bootstrap caching and DNS overrides

Bootstrap hostname lookups now use the same cache and local overrides as ordinary DNS queries, reducing repeated resolution when upstream connections are recreated. No configuration migration is required from v2.0.x. Existing installations with automatic updates may install this release and restart.

## Changes

- Share one in-memory hot cache and one Badger instance between bootstrap and ordinary queries. Both follow `built_in_cache`; unexpired disk entries survive restarts. The cache owner alone closes the shared instance.
- Keep upstream and ECS policies in cache keys. Matching policies can reuse each other's answers, while different policies retain their own results. Existing cache namespaces remain unchanged.
- Apply `custom_records` before bootstrap cache lookups or network requests. Use A records to preconfigure upstream hostnames while keeping their original DoT/DoH names for certificate verification. No additional configuration field is required.
- Preserve TTL expiry, negative caching, concurrent query coalescing, and per-bootstrap SOCKS settings. Zero-TTL answers and failures are not written to cache.
- Return bootstrap lookup errors directly instead of attempting a connection to `0.0.0.0`.
- Include the dependency updates to `github.com/ulikunitz/xz` 0.5.14 and `gopkg.in/yaml.v2` 2.2.8 already merged into main.

## Limits and validation

Bootstrap still queries A records and selects the last A address returned. Multiple configured addresses do not add automatic address rotation or failover; ordinary DNS queries return all matching records. An owner configured only with AAAA or CNAME does not fall back to bootstrap DNS.

Regression tests cover shared cache identity and lifetime, reuse in both directions, reopening persisted entries, ECS isolation, TTL expiry, disabled caching, negative answers, local overrides, and failed bootstrap dialing. Local release checks run `go test -race ./...`, `go vet ./...`, and `go build ./...` before pushing.
