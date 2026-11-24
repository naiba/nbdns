# v2.3.0 — Dashboard security, diagnosis and recent statistics

The dashboard no longer lets other web pages read or change it, gains a DNS diagnosis tool and shows what happened in the last 24 hours instead of only lifetime totals. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Security fixes

- `/api/stats`, `/api/version` and the other dashboard APIs sent `Access-Control-Allow-Origin: *`, so any web page opened on the LAN could read every resolved domain and the client that asked for it. The header is gone; the dashboard and its API are same-origin.
- State-changing requests from other sites are rejected (`http.CrossOriginProtection`), so a web page can no longer reset statistics. Triggering an update check now requires POST; a GET could be fired by an `<img>` on any page.
- The dashboard only answers requests addressed to an IP address, `localhost`, a single-label name or a name under `.lan`, `.local`, `.localdomain`, `.home`, `.home.arpa`, `.internal` or `.localhost`. This stops DNS rebinding, where an attacker's page re-resolves its own name to the dashboard address. Other names, such as a reverse-proxy domain, go in the new `web_allowed_hosts`. DoH is not affected.
- New optional `web_auth` (`username`, `password`) protects the dashboard, its API and pprof with HTTP Basic authentication.

## Dashboard

- Last 24 hours: queries, cache hit rate, blocked and failed queries, and an hourly trend chart. Hourly counters are saved with the statistics and survive restarts. Lifetime totals, now including blocked queries, are shown below.
- DNS diagnosis: for a name and record type, shows custom records, filter rules, whether a dedicated `match` / `match_lists` upstream applies, every matching upstream's answer and latency, the cache entry with its remaining TTL, what the client will receive, and whether dnsmasq rebind protection would drop the answer. Diagnosis asks upstreams directly and changes neither statistics nor the cache.
- Upstreams: only configured upstreams are shown; statistics of removed upstreams are discarded at startup. New last-24-hour error rate and average / P95 latency of valid answers. The last-use time is sent as RFC 3339 and shown in browser local time; it used to be formatted without a time zone in the server's zone, usually UTC on routers.
- Failed domains: Top 50 names whose upstreams all failed, with the failing upstreams or `max_concurrent_queries` overload as the reason.
- Private-address answers: Top 50 names answered with addresses that dnsmasq rebind protection rejects (its `private_net()` ranges), which reach clients behind dnsmasq as empty answers without any dnsmasq log. Cache hits and custom records count.
- Device names: with the new `client_name_server`, client addresses are labeled with PTR names from a LAN resolver such as the DHCP server's dnsmasq. Lookups run in the background and are cached for an hour (10 minutes when no name is known).
- The ECS blackhole repair panel explains repaired answers without addresses instead of showing an empty cell.
- Runtime details move to the footer; labels are in Chinese throughout; the header links to the project and to the release notes of the running version.

## Validation

Tests cover the dnsmasq private ranges (including IPv4-mapped IPv6 and the ranges dnsmasq deliberately allows), counting private answers from upstreams, the cache and custom records, the hourly window across hour changes, expiry, persistence and reset, P95 interpolation and overflow, removal of unconfigured upstreams, failure reasons for upstream errors and overload, diagnosis of every route without side effects, background PTR naming, the absence of CORS headers, POST-only update checks, the host allowlist, cross-site write rejection and Basic authentication. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets; the dashboard was checked in a browser at desktop and phone widths.
