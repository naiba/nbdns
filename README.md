# nbdns

放置于 AdGuard Home 上游，可提升 DNS 解析体验。

![截图](http://inews.gtimg.com/newsapp_ls/0/14876631746/0)

1. 复制 `config.json.example` 到 `config.json`，修改其中配置

   ```text
   is_primary：
      将国内 DNS 的 is_primary 标记为 true
   strategy：
      1 为最全结果
      2 为最快结果
   ```

2. 从 <https://github.com/out0fmemory/qqwry.dat> 处下载 `qqwry_lastest.dat` 放置到 `data` 文件夹中
3. 你的文件层级应该是这样的

   ```shell
   |- nbdns
   |- data
      |- config.json
      |- qqwry_lastest.dat
   ```

4. 启动 `./nbdns`
5. 在 `adguardhome:2333/#dns` 将 `127.0.0.1:8853` 配置到 `上游服务器`

测试命令：

```shell
dig @127.0.0.1 -p 8853 +time=100 +retry=1 www.baidu.com
dig @127.0.0.1 -p 8853 +time=100 +retry=1 www.reddit.com
```
