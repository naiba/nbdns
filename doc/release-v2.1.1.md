# v2.1.1 — ECS blackhole repair and compressed replies

Some services geo-block by ECS: an ECS from a blocked region is answered with an unusable address such as `169.254.0.2`, which dnsmasq rebind protection turns into an empty answer. NbDNS now detects such answers and repairs them automatically, and always name-compresses its replies. No configuration migration is required from v2.0.x. Existing installations with automatic updates may install this release and restart. v2.1.0 was withdrawn; v2.1.1 supersedes it.

## Changes

- When an ECS answer contains special-use A/AAAA addresses (0/8, 10/8, 100.64/10, 127/8, 169.254/16, 172.16/12, 192.168/16, 198.18/15, multicast/reserved, `::`, `::1`, `fe80::/10`, `fc00::/7`), the same upstream is queried once more without ECS. The retry is used only if every address is public.
- Names that genuinely resolve to LAN addresses resolve privately without ECS too, so their answers are kept unchanged. `custom_records` and filtered names never reach this path.
- Repaired names skip ECS for 24 hours from detection, then are probed again so a lifted geo-block returns to ECS-optimised answers. The in-memory table holds up to 4096 names and evicts the entry expiring first.
- New `ecs_blackhole_fallback` option, default `true`. The repairing policy uses a fresh cache namespace, so blackhole answers cached by earlier versions are not served after upgrading. Policies that cannot repair — no ECS, or `ecs_blackhole_fallback: false` — keep their existing caches.
- New dashboard panel "ECS 黑洞自动修复": status (enabled / disabled / no ECS configured), repaired answers, and per-domain blackhole and repaired addresses. Only the answer actually served is counted, so racing upstreams and cache hits are not double-counted. Data is saved in `data/cache/stats.json` and cleared by the reset button.
- Always name-compress DNS replies over UDP, TCP and DoH. `Truncate` disabled compression whenever a reply fitted the client's EDNS size, so a CDN answer with 2 CNAMEs and 8 A records was sent at more than twice its compressed size. dnsmasq queries upstream with EDNS 1232 but relays to clients without EDNS, where anything above 512 bytes becomes an empty truncated (TC) reply that depends on a TCP retry.

## Limits and validation

Repair costs one extra upstream query the first time a blackhole is seen per name per 24 hours. A domain that deliberately answers some regions with a private address while publishing a public address without ECS would be served the public address; disable the option if that is required.

Regression tests cover blackhole replacement with CNAME chains, IPv6, remembered names shared across upstreams, intentional private answers, public answers without retry, the disable switch, address classification, memory eviction, cache namespace isolation, winner-only statistics, cache-hit counting, persistence, reset, the stats API and reply compression. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...`, 32-bit CI targets and release cross-compilation.
