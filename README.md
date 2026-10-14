# NbDNS

[![release](https://img.shields.io/github/v/release/naiba/nbdns?color=brightgreen&label=NbDNS&style=for-the-badge&logo=github)](https://github.com/naiba/nbdns/releases)

:seal: A smart DNS relay that improves DNS resolution accuracy, with a built-in management dashboard. A lightweight alternative to AdGuard Home.

[中文说明](README_zh.md)

![Screenshot](./doc/screenshot.png)

## Quick Start

1. Download the latest release from [releases](https://github.com/naiba/nbdns/releases)
2. Create config file `data/config.json` (see example below)
3. Run `./nbdns`
4. Visit `http://localhost:8854` for the monitoring dashboard
5. DNS TCP/UDP `127.0.0.1:8853`, DoH `http://localhost:8854/dns-query`

**Directory structure:**
```
|- nbdns
|- data
   |- config.json
```

**Test commands:**
```bash
dig @127.0.0.1 -p 8853 www.baidu.com
dig @127.0.0.1 -p 8853 www.google.com
```
[dig](https://help.dyn.com/how-to-use-binds-dig-tool/) tool for Windows

## Configuration Example

Complete configuration JSON Schema: [data/config.schema.json](data/config.schema.json). Editors can reference it using `$schema`; runtime does not automatically perform JSON Schema validation.

```json
{
  "serve_addr": "127.0.0.1:8853",
  "web_addr": "0.0.0.0:8854",
  "ecs": "180.152.0.0/24",
  "timeout": 4,
  "built_in_cache": true,
  "socks_proxy": "192.168.1.254:3838",
  "bootstrap": [
    {"address": "tcp-tls://8.8.8.8:853", "proxy_enabled": true, "ecs": ""}
  ],
  "upstreams": [
    {"address": "tcp-tls://dns.google:853", "proxy_enabled": true},
    {"address": "https://user:pass@doh.example.com/dns-query", "match": ["internal.example", ".internal.example"], "ecs": ""}
  ],
  "doh_server": {
    "username": "admin",
    "password": "secret"
  },
  "filter_lists": ["ads.txt", "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"],
  "filter_update_interval_hours": 24
}
```

### Configuration Reference

| Field            | Description                                                         | Default        |
| ---------------- | ------------------------------------------------------------------- | -------------- |
| `serve_addr`     | DNS service listen address                                          | Required       |
| `web_addr`       | Web dashboard and DoH service port                                  | `0.0.0.0:8854` |
| `ecs` | Global ECS CIDR inherited by upstreams | Empty (no ECS) |
| `timeout`        | Upstream timeout in seconds                                         | `4`            |
| `built_in_cache` | Enable persistent Badger cache and in-memory hot cache              | `false`        |
| `socks_proxy`    | SOCKS5 proxy address                                                | Optional       |
| `bootstrap`      | Bootstrap DNS servers (IP only)                                     | Required       |
| `upstreams`      | Upstream DNS list                                                   | Required       |
| `doh_server`     | DoH server configuration                                           | Optional       |
| `custom_records` | Local DNS overrides, before filters, caches and upstreams | `[]` |
| `filter_lists`   | Local files or HTTPS subscription URLs (multiple supported)          | Optional       |
| `filter_update_interval_hours` | Refresh interval in hours; `-1` disables periodic refresh | `24` |

**Upstream DNS options:**
- `ecs`: Upstream ECS CIDR; omitted or `null` inherits the global setting, `""` disables ECS, and a CIDR overrides it.
- `proxy_enabled`: Connect through SOCKS5 proxy
- `match`: Only match specific domain suffixes

**Domain matching rules:**
- `.` matches all domains
- `a.com` matches only a.com
- `.a.com` matches a.a.com, c.a.com, e.d.a.com, etc.

### Queries and ECS

All matched upstreams race; the first complete, question-matching NOERROR or NXDOMAIN response wins. Network errors, truncated replies, SERVFAIL and REFUSED cannot win. If all fail, return SERVFAIL. Answers are never merged. There is no IP-country filtering, IP database requirement, or strategy setting.

Global `ecs` accepts an IPv4/IPv6 CIDR; omitted or empty means no ECS. Upstream overrides also apply to bootstrap servers; the example explicitly disables bootstrap ECS. Host bits are masked (e.g. `180.152.0.123/24` becomes `180.152.0.0/24`). Client-supplied ECS is always stripped; only administrator-configured ECS is sent, preserving other EDNS options and DO. Changed resolution policies use separate cache namespaces.

`180.152.0.0/24` is a Shanghai Telecom example, not automatic client location detection. ECS discloses a network hint to resolvers and participating authoritative services and does not guarantee the nearest CDN. All racing resolvers should support ECS; otherwise the winning answer may ignore the location hint. Prefer trusted DoH/DoT, optionally through SOCKS. Encryption does not establish resolver trust; even the fastest NXDOMAIN wins.

If `match` rules match, only the matching dedicated upstreams race; otherwise upstreams without `match` are used. Do not put `match: ["."]` on default upstreams, as they would also race with dedicated ones. DNS does not control download routing: local CDN downloads still need direct routing.

### DNS blocklists

`filter_lists` accepts a mixture of local files and HTTPS URLs, for example `"filter_lists": ["ads.txt", "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"]`. Relative paths are resolved against `data/`. At startup, local files and last-known-good downloaded copies are loaded without waiting for the network; subscriptions refresh in the background and then every 24 hours by default (local files are also reloaded). If the first download fails, DNS still starts but those remote rules remain inactive until fetched. Later failures retain the last verified list. Downloads are limited to 32 MiB and cannot use HTTP or redirect to HTTP. The dashboard and `/api/filters` show per-source update status, rule counts, and blocked queries. Lookups use an in-memory domain-label index; allow rules take precedence over block rules.

Supported syntax: common AdGuard DNS rules `||example.com^` (including subdomains), exceptions `@@||safe.example.com^`, and hosts entries such as `0.0.0.0 example.com`, `127.0.0.1 example.com`, or `::1 example.com`. Lines beginning with `#` or `!` are comments. Wildcards, regular expressions, `$` modifiers, and the rest of the full AdGuard syntax are **not supported**; the number of skipped rules is logged at startup. Blocked names receive NXDOMAIN without querying upstream. DNS-only filtering cannot reliably remove YouTube ads served from video domains.

With `built_in_cache` enabled, frequently used DNS replies are served from a hot in-memory cache (at most 2,048 entries and approximately 8 MiB of response data); misses use the persistent Badger cache. These limits exclude Go object overhead, Badger, and subscription lists and are **not** a process RSS limit.

## Features

### :chart_with_upwards_trend: Web Monitoring Dashboard
Visit `http://localhost:8854` to view:
- Runtime status (uptime, memory, goroutines, GC)
- DNS query statistics (total queries, cache hit rate, failures)
- DNS subscription status (rule counts, refresh errors and blocked queries)
- Upstream server status (queries, error rate, last used)
- Top 10 clients, Top 50 resolved/blocked domains, and per-domain client request breakdowns
- Statistics reset

### :lock: DoH (DNS over HTTPS)
DoH service shares the same port as the web dashboard, accessible at: `/dns-query`

The built-in listener uses plain HTTP. Put it behind an HTTPS reverse proxy and restrict dashboard access before using DoH or Basic Auth remotely. Both RFC 8484 GET and POST requests are supported.

**Configuration:**
```json
{
  "doh_server": {
    "username": "admin",
    "password": "secret"
  }
}
```

**Test:**
```bash
curl -v -H "Accept: application/dns-message" \
  -u "user:password" \
  "http://localhost:8854/dns-query?dns=AAABAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB"
```

**Browser setup (Firefox):**
Settings → Network Settings → Enable DNS over HTTPS → Custom → `http://your-server:8854/dns-query`

## Deployment

### :whale: Docker
```bash
docker run --name nbdns --restart always -d \
  -v /path/to/data:/nbdns/data \
  -p 8853:8853/udp \
  -p 8854:8854 \
  ghcr.io/naiba/nbdns
```

### :package: OpenWRT Auto-start
Download the appropriate binary from releases, extract and place it in `/root`, then run `chmod -R 777 /root/nbdns` to grant permissions. Create `/etc/init.d/nbdns`:

```shell
#!/bin/sh /etc/rc.common
USE_PROCD=1
# After network starts
START=21
# Before network stops
STOP=89

cmd=/root/nbdns/nbdns
name=nbdns
pid_file="/var/run/${name}.pid"

start_service() {
    echo "Starting ${name}"
    procd_open_instance
    procd_set_param command ${cmd}
    procd_set_param respawn

    # respawn automatically if something died, be careful if you have an alternative process supervisor
    # if process exits sooner than respawn_threshold, it is considered crashed and after 5 retries the service is stopped
    # if process finishes later than respawn_threshold, it is restarted unconditionally, regardless of error code
    # notice that this is literal respawning of the process, no in a respawn-on-failure sense
    procd_set_param respawn ${respawn_threshold:-3600} ${respawn_timeout:-5} ${respawn_retry:-5}

    procd_set_param stdout 1             # forward stdout of the command to logd
    procd_set_param stderr 1             # same for stderr
    procd_set_param pidfile ${pid_file}  # write a pid file on instance start and remove it on stop
    procd_close_instance
    echo "${name} has been started"
}
```

Grant execute permission: `chmod +x /etc/init.d/nbdns`, then enable and start: `/etc/init.d/nbdns enable && /etc/init.d/nbdns start`

## Custom DNS records

`custom_records` supports multiple records and multiple values per owner/type. Exact names match case-insensitively, ignoring a trailing dot; wildcards are unsupported. Matched IN queries bypass filters, caches and upstreams. Missing types return NOERROR with no answers and never reach upstreams. Supported types: A, AAAA, CNAME, TXT, MX, SRV, PTR, NS. `value` uses DNS presentation syntax; `ttl` is in seconds (0 disables caching). A CNAME must be the only record at its owner and is returned without recursively resolving its target. Overrides apply to business queries, not bootstrap. Restart after configuration changes.

```json
{
  "custom_records": [
    {
      "name": "nas.home.arpa",
      "type": "A",
      "value": "192.168.1.10",
      "ttl": 60
    },
    {
      "name": "nas.home.arpa",
      "type": "AAAA",
      "value": "fd00::10",
      "ttl": 60
    },
    {
      "name": "alias.home.arpa",
      "type": "CNAME",
      "value": "nas.home.arpa.",
      "ttl": 60
    }
  ]
}
```

### `.local` and mDNS

NbDNS does not currently support mDNS (UDP 5353 multicast), Bonjour / DNS-SD service discovery, or automatic LAN device discovery. Custom records can answer ordinary `.local` DNS queries sent directly to NbDNS, but operating systems may route `.local` queries through mDNS instead. A successful `dig @NbDNS nas.local` does not guarantee resolution in applications. Prefer names such as `nas.home.arpa` for static local overrides.
