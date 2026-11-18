# v2.1.4 — Repair ECS refusals

Some GeoDNS names answer REFUSED to every query that carries ECS, yet resolve normally without it. nbdns treated REFUSED as a failed upstream reply, so clients received SERVFAIL for these names, repeatedly. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- When an ECS query is answered REFUSED, the same upstream is queried once more without ECS. The retry is used when it is a usable NOERROR or NXDOMAIN reply without special-use addresses; a name refused with and without ECS stays REFUSED and is not remembered.
- Repaired names skip ECS for 24 hours, sharing the memory used for ECS blackholes, and the original reason is kept for remembered names.
- The dashboard panel records the reason of every repair; refusals are shown as `REFUSED` in the "ECS 返回" column. The reason is saved in `stats.json`.
- `ecs_blackhole_fallback` controls both repairs.

## Limits and validation

A retry without ECS returns the answer the authority gives the resolver itself, which for GeoDNS that ignores ECS may be another region's CDN. Services that only hand out regional lines to resolvers located in that region need such a resolver; route their domains to it with an upstream `match`.

Regression tests cover repair and remembering of refused names, names refused regardless of ECS, the disable switch, the reason in fallback reports, statistics persistence and handler replies that used to become SERVFAIL. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...`, 32-bit CI targets and release cross-compilation.
