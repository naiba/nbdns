# v2.2.0 — Domain list subscriptions for upstreams

Upstreams can now take their `match` domains from subscribed or local domain lists, so large lists such as dnsmasq-china-list route to a dedicated upstream without hand-written rules. Subscriptions download less and write less to flash storage. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- New upstream field `match_lists`: HTTPS subscriptions or files under `data/`, combined with `match`. Supported entries: dnsmasq `server=/`, `nftset=/`, `ipset=/`, `address=/` lines (several domains per line), plain domains including `.example.com` and `+.example.com`, `domain:`, `full:` (exact) and Clash `DOMAIN-SUFFIX,` / `DOMAIN,`. `regexp:` and similar entries are counted as unsupported.
- Lists are matched with the label tree used by filters: one walk per query, about 0.1 µs and 7 MiB of memory for 110,000 domains, with no allocation per lookup.
- Lists follow the subscription lifecycle of filters: cached copies load at startup without network access, downloads run in the background every `filter_update_interval_hours`, a failed download keeps the last good copy, and caches live in `data/match_lists/`, separate from filter caches. Until a list first downloads, its names use the default upstreams. A list used by several upstreams is downloaded and cached once.
- Subscriptions, including filter lists, now send `If-None-Match`: an unchanged list costs a 304 instead of a download, identical content is never rewritten, and when no source changed the index is not rebuilt. A deleted cache file is restored on the next refresh even if the list is unchanged.
- The dashboard lists domain list subscriptions with entry counts and status under the upstream servers.
- Configuring `match_lists` on an upstream changes the cache namespace of that policy; configurations without it keep their caches.

## Validation

Regression tests cover every supported format, exact versus suffix entries, routing of listed names to the dedicated upstream and of others to the default upstreams, fallback before the first download, shared managers per list, rejection of plain HTTP, conditional requests, skipping unchanged writes for domain lists and filter subscriptions, restoring a deleted cache, the cache namespace and the dashboard API. A benchmark covers lookups in a 110,000-entry list. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...`, 32-bit CI targets and release cross-compilation.
