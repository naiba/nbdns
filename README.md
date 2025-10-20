# NbDNS

[![release](https://img.shields.io/github/v/release/naiba/nbdns?color=brightgreen&label=NbDNS&style=for-the-badge&logo=github)](https://github.com/naiba/nbdns/releases)

:seal: 一个聪明的 DNS 中继器，可提升 DNS 解析准确性，自带管理面板，可替代 AdguardHome。

![截图](./doc/screenshot.png)

## 快速开始

1. 从 [releases](https://github.com/naiba/nbdns/releases) 下载最新版本
2. 下载 [china_ip_list.txt](https://github.com/17mon/china_ip_list/raw/master/china_ip_list.txt) 到 `data` 文件夹
3. 创建配置文件 `data/config.json`（参考下方配置示例）
4. 启动 `./nbdns`
5. 访问 `http://localhost:8854` 查看监控面板
6. DNS TCP/UDP `127.0.0.1:8853`, DoH `http://localhost:8854/dns-query`

**文件结构：**
```
|- nbdns
|- data
   |- config.json
   |- china_ip_list.txt
```

**测试命令：**
```bash
dig @127.0.0.1 -p 8853 www.baidu.com
dig @127.0.0.1 -p 8853 www.google.com
```
Windows 上的 [dig](https://help.dyn.com/how-to-use-binds-dig-tool/) 工具

## 配置示例

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
  "blacklist": [".bing.com"]
}
```

### 配置说明

| 字段             | 说明                                                 | 默认值         |
| ---------------- | ---------------------------------------------------- | -------------- |
| `serve_addr`     | DNS 服务监听地址                                     | 必填           |
| `web_addr`       | Web 面板和 DoH 服务端口                              | `0.0.0.0:8854` |
| `strategy`       | 查询策略：1-最全结果，2-最快结果（推荐），3-任一结果 | `2`            |
| `timeout`        | 上游超时时间（秒）                                   | `4`            |
| `built_in_cache` | 启用内建缓存                                         | `false`        |
| `socks_proxy`    | SOCKS5 代理地址                                      | 可选           |
| `bootstrap`      | Bootstrap DNS 服务器（仅支持 IP）                    | 必填           |
| `upstreams`      | 上游 DNS 列表                                        | 必填           |
| `doh_server`     | DoH 服务配置                                         | 可选           |
| `blacklist`      | 域名黑名单（强制使用非 primary DNS）                 | 可选           |

**上游 DNS 配置：**
- `is_primary`: 标记国内 DNS
- `use_socks`: 通过 SOCKS5 代理连接
- `match`: 仅匹配特定域名后缀

**域名匹配规则：**
- `.` 匹配所有
- `a.com` 仅匹配 a.com
- `.a.com` 匹配 a.a.com, c.a.com, e.d.a.com 等

## 功能特性

### :chart_with_upwards_trend: Web 监控面板
访问 `http://localhost:8854` 查看：
- 运行时状态（运行时长、内存、Goroutines、GC）
- DNS 查询统计（总查询数、缓存命中率、失败数）
- 上游服务器状态（查询数、错误率、最后使用时间）
- Top 客户端 IP 和查询域名排行
- 统计数据重置功能

### :lock: DoH (DNS over HTTPS)
DoH 服务与 Web 面板共用端口，访问路径：`/dns-query`

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
