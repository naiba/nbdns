# NbDNS

[![release](https://img.shields.io/github/v/release/naiba/nbdns?color=brightgreen&label=NbDNS&style=for-the-badge&logo=github)](https://github.com/naiba/nbdns/releases)

:seal: A smart DNS relay that improves DNS resolution accuracy, with a built-in management dashboard. A lightweight alternative to AdGuard Home.

[中文说明](README_zh.md)

![Screenshot](./doc/screenshot.png)

## Quick Start

1. Download the latest release from [releases](https://github.com/naiba/nbdns/releases)
2. Download [china.txt](https://raw.githubusercontent.com/gaoyifan/china-operator-ip/refs/heads/ip-lists/china.txt) and save it as `china_ip_list.txt` in the `data` folder
   ```shell
   wget https://raw.githubusercontent.com/gaoyifan/china-operator-ip/refs/heads/ip-lists/china.txt -O data/china_ip_list.txt
   ```
3. Create config file `data/config.json` (see example below)
4. Run `./nbdns`
5. Visit `http://localhost:8854` for the monitoring dashboard
6. DNS TCP/UDP `127.0.0.1:8853`, DoH `http://localhost:8854/dns-query`

**Directory structure:**
```
|- nbdns
|- data
   |- config.json
   |- china_ip_list.txt
```

**Test commands:**
```bash
dig @127.0.0.1 -p 8853 www.baidu.com
dig @127.0.0.1 -p 8853 www.google.com
```
[dig](https://help.dyn.com/how-to-use-binds-dig-tool/) tool for Windows

## Configuration Example

```json
{
  "serve_addr": "127.0.0.1:8853",
  "web_addr": "0.0.0.0:8854",
  "strategy": 2,
  "timeout": 4,
  "built_in_cache": true,
  "socks_proxy": "192.168.1.254:3838",
  "bootstrap": [
    {"address": "tcp://8.8.4.4:53"},
    {"address": "tcp://1.0.0.1:53"}
  ],
  "upstreams": [
    {"address": "udp://223.5.5.5:53", "is_primary": true},
    {"address": "udp://223.6.6.6:53", "is_primary": true},
    {"address": "tcp-tls://dns.google:853", "use_socks": true},
    {"address": "tcp-tls://one.one.one.one:853", "use_socks": true},
    {"address": "https://user:pass@doh.example.com/dns-query", "match": [".onion"]}
  ],
  "doh_server": {
    "username": "admin",
    "password": "secret"
  },
  "blacklist": [".bing.com"],
  "filter_lists": ["ads.txt", "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"],
  "filter_update_interval_hours": 24
}
```

### Configuration Reference

| Field            | Description                                                         | Default        |
| ---------------- | ------------------------------------------------------------------- | -------------- |
| `serve_addr`     | DNS service listen address                                          | Required       |
| `web_addr`       | Web dashboard and DoH service port                                  | `0.0.0.0:8854` |
| `strategy`       | Query strategy: 1-most complete, 2-fastest (recommended), 3-any    | `2`            |
| `timeout`        | Upstream timeout in seconds                                         | `4`            |
| `built_in_cache` | Enable persistent Badger cache and in-memory hot cache              | `false`        |
| `socks_proxy`    | SOCKS5 proxy address                                                | Optional       |
| `bootstrap`      | Bootstrap DNS servers (IP only)                                     | Required       |
| `upstreams`      | Upstream DNS list                                                   | Required       |
| `doh_server`     | DoH server configuration                                           | Optional       |
| `blacklist`      | Domain blacklist (force non-primary DNS)                            | Optional       |
| `filter_lists`   | Local files or HTTPS subscription URLs (multiple supported)          | Optional       |
| `filter_update_interval_hours` | Refresh interval in hours; `-1` disables periodic refresh | `24` |

**Upstream DNS options:**
- `is_primary`: Mark as domestic/primary DNS
- `use_socks`: Connect through SOCKS5 proxy
- `match`: Only match specific domain suffixes

**Domain matching rules:**
- `.` matches all domains
- `a.com` matches only a.com
- `.a.com` matches a.a.com, c.a.com, e.d.a.com, etc.

### DNS blocklists

`filter_lists` accepts a mixture of local files and HTTPS URLs, for example `"filter_lists": ["ads.txt", "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"]`. Relative paths are resolved against `data/`. At startup, local files and last-known-good downloaded copies are loaded without waiting for the network; subscriptions refresh in the background and then every 24 hours by default (local files are also reloaded). If the first download fails, DNS still starts but those remote rules remain inactive until fetched. Later failures retain the last verified list. Downloads are limited to 32 MiB and cannot use HTTP or redirect to HTTP. The dashboard and `/api/filters` show per-source update status, rule counts, and blocked queries. Lookups use an in-memory domain-label index; allow rules take precedence over block rules.

Supported syntax: common AdGuard DNS rules `||example.com^` (including subdomains), exceptions `@@||safe.example.com^`, and hosts entries such as `0.0.0.0 example.com`, `127.0.0.1 example.com`, or `::1 example.com`. Lines beginning with `#` or `!` are comments. Wildcards, regular expressions, `$` modifiers, and the rest of the full AdGuard syntax are **not supported**; the number of skipped rules is logged at startup. Blocked names receive NXDOMAIN without querying upstream. DNS-only filtering cannot reliably remove YouTube ads served from video domains. The separate `blacklist` setting only changes upstream routing; it does **not** block domains.

With `built_in_cache` enabled, frequently used DNS replies are served from a hot in-memory cache (at most 2,048 entries and approximately 8 MiB of response data); misses use the persistent Badger cache. These limits exclude Go object overhead, Badger, and subscription lists and are **not** a process RSS limit.

## Features

### :chart_with_upwards_trend: Web Monitoring Dashboard
Visit `http://localhost:8854` to view:
- Runtime status (uptime, memory, goroutines, GC)
- DNS query statistics (total queries, cache hit rate, failures)
- DNS subscription status (rule counts, refresh errors and blocked queries)
- Upstream server status (queries, error rate, last used)
- Top client IPs and queried domains
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
