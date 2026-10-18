# NbDNS

[![release](https://img.shields.io/github/v/release/naiba/nbdns?color=brightgreen&label=NbDNS&style=for-the-badge&logo=github)](https://github.com/naiba/nbdns/releases)

:seal: 一个聪明的 DNS 中继器，可提升 DNS 解析准确性，自带管理面板，可替代 AdguardHome。

[English](README.md)

![截图](./doc/screenshot.png)

## 快速开始

1. 从 [releases](https://github.com/naiba/nbdns/releases) 下载最新版本
2. 创建配置文件 `data/config.json`（参考下方配置示例）
3. 启动 `./nbdns`
4. 访问 `http://localhost:8854` 查看监控面板
5. DNS TCP/UDP `127.0.0.1:8853`, DoH `http://localhost:8854/dns-query`

**文件结构：**
```
|- nbdns
|- data
   |- config.json
```

**测试命令：**
```bash
dig @127.0.0.1 -p 8853 www.baidu.com
dig @127.0.0.1 -p 8853 www.google.com
```
Windows 上的 [dig](https://help.dyn.com/how-to-use-binds-dig-tool/) 工具

## 配置示例

完整配置 JSON Schema：[data/config.schema.json](data/config.schema.json)。编辑器可通过 `$schema` 引用；运行时不自动执行 JSON Schema 校验。

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

### 配置说明

| 字段             | 说明                                                 | 默认值         |
| ---------------- | ---------------------------------------------------- | -------------- |
| `serve_addr`     | DNS 服务监听地址                                     | 必填           |
| `web_addr`       | Web 面板和 DoH 服务端口                              | `0.0.0.0:8854` |
| `ecs` | 全局 ECS CIDR，上游未配置时继承 | 空（不发送） |
| `timeout`        | 上游超时时间（秒）                                   | `4`            |
| `max_concurrent_queries` | 等待回源的最大并发数，含合并请求的等待者；超限返回 SERVFAIL 或可用旧缓存 | `64` |
| `max_active_connections` | 每个 TCP/DoT 连接池或 DoH 上游的最大连接数 | `50` |
| `max_idle_connections` | 每个 TCP/DoT 连接池或 DoH 上游的最大空闲连接数 | `20` |
| `built_in_cache` | 启用 Badger 持久缓存与内存热缓存                     | `false`        |
| `socks_proxy`    | SOCKS5 代理地址                                      | 可选           |
| `bootstrap`      | Bootstrap DNS 服务器（仅支持 IP）                    | 必填           |
| `upstreams`      | 上游 DNS 列表                                        | 必填           |
| `doh_server`     | DoH 服务配置                                         | 可选           |
| `custom_records` | 本地 DNS 覆盖记录，优先于过滤、缓存及上游 | `[]` |
| `filter_lists`   | 本地文件或 HTTPS 订阅 URL（可配置多个）            | 可选           |
| `filter_update_interval_hours` | 自动更新间隔（小时）；`-1` 关闭周期更新 | `24` |

**上游 DNS 配置：**
- `ecs`: 上游 ECS CIDR；省略或 `null` 继承全局，`""` 禁用，其他 CIDR 覆盖全局。
- `proxy_enabled`: 通过 SOCKS5 代理连接
- `match`: 仅匹配特定域名后缀

**域名匹配规则：**
- `.` 匹配所有
- `a.com` 仅匹配 a.com
- `.a.com` 匹配 a.a.com, c.a.com, e.d.a.com 等

### 查询与 ECS

所有匹配上游并发查询，采用第一个完整、请求匹配的 NOERROR 或 NXDOMAIN 应答；网络错误、截断响应、SERVFAIL 和 REFUSED 不会抢先胜出，全部失败时返回 SERVFAIL。不合并不同上游的答案，不进行 IP 国家判断，不需要 IP 网段文件，也没有查询模式。

全局 `ecs` 为 IPv4/IPv6 CIDR，省略或空字符串表示不发送 ECS。上游 `ecs` 可以继承、覆盖或显式禁用；bootstrap 使用相同继承规则，示例显式禁用以避免不必要的位置提示。主机位会归零，例如 `180.152.0.123/24` 实际发送 `180.152.0.0/24`。客户端自行携带的 ECS 总会删除，只发送管理员配置的值；其他 EDNS 选项与 DO 标记保留。解析策略改变后使用独立的缓存命名空间。

`180.152.0.0/24` 是上海电信代表网段示例，并非自动检测的用户位置。ECS 向解析器及支持它的权威服务披露网段提示，不保证最近 CDN；配置上游必须支持 ECS，否则竞速胜出的答案可能未按指定位置调度。优先使用可信 DoH/DoT，必要时启用 SOCKS；加密不等于解析器内容可信。仅添加可信上游，因为最快的 NXDOMAIN 也会被采用。

`match` 命中时仅查询所有命中的专用上游，否则查询无 `match` 的默认上游。默认上游不要设置 `match: ["."]`，否则会与其他专用上游共同竞速。DNS 不控制下载出口；本地 CDN 下载仍须在路由/代理端直连。

### DNS 广告拦截名单

`filter_lists` 可以混合配置本地文件和 HTTPS URL，例如 `"filter_lists": ["ads.txt", "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"]`；相对路径以 `data/` 为基准。启动时先加载本地文件和上次成功下载的订阅缓存，随后在后台获取最新名单，默认每 24 小时自动更新（本地文件也会重新读取）。首次无缓存且网络不可用时 DNS 仍会启动，远程规则要等下载成功才生效；下载失败则保留上一份有效规则。单个远程响应上限为 32 MiB，不接受 HTTP URL 或降级到 HTTP 的重定向。每个来源的规则数、最近更新时间、错误及本次运行拦截数在面板和 `/api/filters` 中查看。查询时只访问内存中的域名标签索引，白名单规则优先于拦截规则。

支持常见 AdGuard DNS 规则 `||example.com^`（含子域名）、例外规则 `@@||safe.example.com^`，以及 hosts 文件中的 `0.0.0.0 example.com`、`127.0.0.1 example.com`、`::1 example.com`；`#`、`!` 开头的注释会跳过。**目前不支持**通配符、正则、`$` 修饰符等完整 AdGuard 语法；启动日志会报告跳过的规则数。命中时返回 NXDOMAIN，不向上游发起查询。此功能无法稳定拦截与正常视频共用域名的 YouTube 广告。

启用 `built_in_cache` 后，常用 DNS 应答优先从内存热缓存读取（最多 2048 条、估算应答数据 8 MiB），未命中时回退到 Badger 持久缓存。上述热缓存额度不包含 Go 对象、Badger 和订阅名单，因此**不是进程内存的硬上限**。

## 功能特性

### :chart_with_upwards_trend: Web 监控面板
访问 `http://localhost:8854` 查看：
- 运行时状态（运行时长、内存、Goroutines、GC）
- DNS 查询统计（总查询数、缓存命中率、失败数）
- DNS 拦截订阅状态（规则数、更新结果和拦截次数）
- 上游服务器状态（查询数、错误率、最后使用时间）
- Top 10 客户端、Top 50 已解析/已拦截域名排行，以及每个域名的客户端请求分布
- 统计数据重置功能

### :lock: DoH (DNS over HTTPS)
DoH 服务与 Web 面板共用端口，访问路径：`/dns-query`

内置服务只监听 HTTP；在非本机使用 DoH 或 Basic Auth 时，请在前面部署 HTTPS 反向代理并限制管理面板访问。支持 RFC 8484 的 GET 与 POST 请求。

**配置示例：**
```json
{
  "doh_server": {
    "username": "admin",
    "password": "secret"
  }
}
```

**测试：**
```bash
curl -v -H "Accept: application/dns-message" \
  -u "user:password" \
  "http://localhost:8854/dns-query?dns=AAABAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB"
```

**浏览器配置（Firefox）：**
设置 → 网络设置 → 启用基于 HTTPS 的 DNS → 自定义 → `http://your-server:8854/dns-query`

## 部署

### :whale: Docker
```bash
docker run --name nbdns --restart always -d \
  -v /path/to/data:/nbdns/data \
  -p 8853:8853/udp \
  -p 8854:8854 \
  ghcr.io/naiba/nbdns
```

### :package: OpenWRT 自启动
首先在 release 下载对应的二进制解压 zip 包后放置到 `/root`，然后 `chmod -R 777 /root/nbdns` 赋予执行权限，然后创建 `/etc/init.d/nbdns`：

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

赋予执行权限 `chmod +x /etc/init.d/nbdns` 然后启动服务 `/etc/init.d/nbdns enable && /etc/init.d/nbdns start`

## 自定义解析记录

`custom_records` 可配置多条记录，同域名同类型可返回多个值。精确匹配域名，忽略大小写和末尾点，不支持通配符。IN 查询命中后优先于过滤、缓存与上游直接返回；没有对应类型时返回 NOERROR 空答案，不访问上游。支持 A、AAAA、CNAME、TXT、MX、SRV、PTR、NS；`value` 使用标准 DNS 文本格式，`ttl` 单位为秒（0 表示不缓存）。CNAME 必须是该域名唯一的记录；返回别名但不递归追踪目标。仅应用于业务查询，不应用于 bootstrap。配置修改后需重启。

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

### `.local` 与 mDNS

NbDNS 当前不支持 mDNS（UDP 5353 组播）或 Bonjour / DNS-SD 服务发现，也不会自动发现局域网设备。自定义记录可以应答直接发送给 NbDNS 的 `.local` 普通 DNS 查询，但系统可能将 `.local` 查询交给 mDNS 而不访问 NbDNS；因此 `dig @NbDNS nas.local` 成功不代表系统应用能解析。静态内网名称建议使用 `nas.home.arpa`。
