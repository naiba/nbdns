# NbDNS

[![release](https://img.shields.io/github/v/release/naiba/nbdns?color=brightgreen&label=NbDNS&style=for-the-badge&logo=github)](https://github.com/naiba/nbdns/releases)

:seal: 一个聪明的 DNS 中继器，放置于 AdGuard Home 上游，可提升 DNS 解析准确性。

![截图](http://inews.gtimg.com/newsapp_ls/0/14876631746/0)

1. 从 [releases](https://github.com/naiba/nbdns/releases) 下载最新的 `nbdns`
2. 复制 `data/config.json.example` 到 `data/config.json`，修改其中配置

   ```yaml
   socks_proxy: "192.168.55.254:9050" # 你的路由上的 socks5 服务
   strategy: 2
      # 1 - 最全结果
      # 2 - 最快结果（推荐）
      # 3 - 任一结果（不建议使用）
   timeout: 4 # 超时时间（秒）
   built_in_cache: false # 启用内建缓存
   bootstrap: "223.5.5.5" # 解析上游 DNS (dot/doh) 的 IP 使用的 bootstrap 服务器
   upstreams: 上游 DNS 列表（首推使用 tcp-tls，启用 tls 的服务器必须使用主机名）
      is_primary: 将国内 DNS 的 is_primary 标记为 true
      use_socks: 可以为非 is_primary 启用 socks5
      match: # 此上游仅解析匹配的域名列表，比如 Tor 的 onion，可以专门某个后缀定义上游
         - ".onion."
   doh_server:
      host: 0.0.0.0:8053 # DoH 服务器端口
      username: user # 可选的 basic auth
      password: pass 
   blacklist:
      - ".bing.com" # 强制 bing 通过非 primary 服务器解析
      - ".bing.com."
   ```

3. 从 <https://github.com/17mon/china_ip_list/raw/master/china_ip_list.txt> 处下载 `china_ip_list.txt` 放置到 `data` 文件夹中
4. 你的文件层级应该是这样的

   ```shell
   |- nbdns
   |- data
      |- config.json
      |- china_ip_list.txt
   ```

5. 启动 `./nbdns`
6. 在 `adguardhome:2333/#dns` 将 `127.0.0.1:8853` 配置到 `上游服务器`

在运行 `nbdns` 机器上测试：

```shell
dig @127.0.0.1 -p 8853 +time=100 +retry=0 www.baidu.com
dig @127.0.0.1 -p 8853 +time=100 +retry=0 www.reddit.com
```

Windows 上的 [dig](https://help.dyn.com/how-to-use-binds-dig-tool/) 工具

## FAQ

### 匹配规则

```python
'.' => 匹配所有
'a.com' => a.com
'.a.com' => a.a.com c.a.com e.d.a.com
```

### Docker

```shell
docker run -name nbdns --restart always -d -v data路径:/nbdns/data -p 配置的serve_addr端口:配置的serve_addr端口/udp ghcr.io/naiba/nbdns
```

### OpenWRT 自启动

首先在 release 下载对应的二进制解压 zip 包后放置到 `/root`，然后 `chmod -R 777 /root/nbdns` 赋予执行权限，然后创建 `/etc/init.d/nbdns`：

```shell
#!/bin/sh /etc/rc.common

START=99 # 执行的顺序，按照字符串顺序排序并不是数字排序
STOP=15
SERVICE=nbdns
PROG=/root/nbdns
USE_PROCD=1 # 使用procd启动

# start_service 函数必须要重新定义
start_service()
{
    echo service nbdns start
    procd_open_instance  # 创建一个实例， 在 procd 看来一个应用程序可以多个实例
    # ubus call service list 可以查看实例
    procd_set_param command $PROG # mycode执行的命令是"/app/mycode"， 若后面有参数可以直接在后面加上
    procd_set_param respawn # 定义respawn参数，告知procd当mycode程序退出后尝试进行重启
    # procd_close_instance # 关闭实例
}
stop_service() {
    killall nbdns
}

restart() {
 stop
 sleep 2
 start
}
```

赋予执行权限 `chmod +x /etc/init.d/nbdns` 然后启动服务 `/etc/init.d/nbdns enable && /etc/init.d/nbdns start`
