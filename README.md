# biliaudio

把 B 站视频的音频解析出来，支持三种用法：

- **命令行**：`biliaudio BV1GJ411x7h7` —— 批量下载、断点续传
- **网页版**：`biliaudio serve` —— 手机浏览器直接访问，在线试听与下载
- **油猴脚本**：`userscript/biliaudio.user.js` —— 在 B 站视频页直接下载，无需本地服务

命令行和网页版共用同一套解析与代理逻辑，纯 Go 标准库实现，**零第三方依赖**。



## 快速开始

```bash
# 需要 Go 1.21+（开发环境为 1.25）
make build          # 生成 ./bin/biliaudio
make install        # 或者安装到 $GOBIN

# 下载单个视频（最高音质）
biliaudio BV1GJ411x7h7

# 指定输出目录与文件名模板
biliaudio -o ~/Music -f "{author} - {title}" "https://www.bilibili.com/video/BV1GJ411x7h7"

# 只下载第 2 个分 P
biliaudio -p 2 BV1Js411o7Fs

# 一次多个 / 直接贴分享文案 / 管道输入
biliaudio BV1GJ411x7h7 BV1xx411c7mD
biliaudio "【标题】https://b23.tv/abc123XY 复制打开"
cat links.txt | biliaudio -o ~/Music

# 只解析不下载，先看看会存到哪
biliaudio -n BV1GJ411x7h7

# 启动网页版
biliaudio serve -addr :8080
```

## 命令行参数

| 参数 | 说明 |
| --- | --- |
| `-o, -output <目录>` | 输出目录，默认当前目录 |
| `-f, -filename <模板>` | 文件名模板，默认 `{page} - {title}` |
| `-p <范围>` | 只下载指定分 P：`-p 1`、`-p 1,3-5` |
| `-n, -dry-run` | 只解析并显示输出路径，不下载 |
| `-force` | 已存在同名文件时也重新下载 |
| `-q` | 安静模式，不显示进度条 |
| `-cookie <Cookie>` | B 站登录态，默认读环境变量 `BILI_COOKIE` |
| `-timeout <时长>` | 整体超时，默认 `5m`，`0` 表示不限制 |
| `-version` | 显示版本号 |

模板变量：`{bvid}` `{title}` `{author}` `{page}` `{part}` `{index}` `{date}`。
单 P 视频的 `{page}` 为空，所以默认模板不会出现多余前缀。

退出码：`0` 成功，`1` 有下载失败或解析失败，`2` 用法错误。

## 网页版

```bash
biliaudio serve -addr :8080                    # 本机 + 局域网
biliaudio serve -addr 127.0.0.1:8080           # 只监听本机，前面挂 Nginx
biliaudio serve -addr :8080 -token mysecret    # 手机访问 http://<IP>:8080/?token=mysecret
```

启动后会打印可访问地址（含局域网 IP），手机连同一个 Wi-Fi 直接打开即可。
页面是移动端优先的响应式布局：解析结果卡片 + 底部吸附的原生音频播放器，
可试听、拖动进度条、点击下载。

**静态资源已用 `go:embed` 编进二进制**——模板、CSS、JS、图标全部内嵌，
不需要 APlayer 之类的外部 CDN，因此部署时只需要拷贝一个可执行文件。

| 参数 | 说明 |
| --- | --- |
| `-addr <地址>` | 监听地址，默认 `:8080` |
| `-token <令牌>` | 访问令牌，留空则自动生成一个随机值（**每次重启都会变**，建议固定） |
| `-cookie <Cookie>` | B 站登录态，默认读 `BILI_COOKIE` |
| `-timeout <时长>` | 单次请求超时，默认 `10m` |

环境变量：`BILI_COOKIE`、`BILIAUDIO_TOKEN`、`BILIAUDIO_DEBUG`（输出调试日志）。

路由：`/` 首页、`POST /api/parse` 表单解析（禁用 JS 也能用）、
`GET /api/parse` JSON 接口、`GET /api/proxy/<base64url>` 音频转发、`/healthz` 探活。

### 确认线上跑的是哪一版

改完代码却「看起来没生效」是最费时间的排查，所以版本信息是可核验的：

```bash
biliaudio -version              # 本机二进制：v1.0.1 (7a292a2) 2026-10-12T17:18:49Z
curl -s localhost:8080/healthz | python3 -m json.tool
# {"ok": true, "version": "v1.0.1", "commit": "7a292a2",
#  "date": "...",  "go": "go1.25.5", "os": "linux", "arch": "amd64",
#  "started": "2026-10-13 01:18:55"}
```

`started` 是进程启动时间——如果它比你重启的时间还早，说明服务其实没重启成功。

### 部署到 VPS

```bash
# 本地交叉编译出静态二进制
make build-linux GOOS=linux GOARCH=amd64     # 或 GOARCH=arm64
scp bin/biliaudio-linux-amd64 user@vps:/usr/local/bin/biliaudio
```

推荐用 systemd 常驻，并只监听本机、由 Nginx 负责 HTTPS：

```ini
# /etc/systemd/system/biliaudio.service
[Unit]
Description=biliaudio web
After=network.target

[Service]
ExecStart=/usr/local/bin/biliaudio serve -addr 127.0.0.1:8080 -token 换成你的令牌
Environment=BILI_COOKIE=SESSDATA=可选的登录态
Restart=always
User=www-data

[Install]
WantedBy=multi-user.target
```

```nginx
# /etc/nginx/sites-available/biliaudio
server {
    listen 443 ssl http2;
    server_name audio.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # 音频是流式转发，关掉缓冲才能拖动进度条
        proxy_buffering off;
        proxy_read_timeout 600s;
    }
}
```

```bash
sudo systemctl enable --now biliaudio
```

## 特性

- **最高音质**：`fnval=16|4048`（DASH + Hi-Res + 杜比全景声），自动在
  `dash.audio` 里挑 `bandwidth` 最大的一档
- **断点续传**：先写 `.part` 临时文件，用 `Range` 续传；服务端忽略 Range 时
  自动从头重写，不会把新数据追加到旧数据后面；完成后原子 `rename`
- **失败重试**：网络中断会重试 4 次并指数退避，继续时从断点开始
- **输入形态**：BV 号、av 号、`b23.tv` 短链（HEAD 解析跳转）、完整链接，
  以及把它们夹在任意文本里（整个分享文案直接粘）
- **文件名清理**：保留中日韩文字，剔除 `/ \ : ? * " < > |` 等非法字符，
  按字符（而非字节）截断，不会切坏汉字
- **中断安全**：Ctrl-C 保留 `.part`，下次重跑自动续传
- **适合管道**：输出重定向时自动关闭颜色与动态进度条

### 网页版的额外设计

- **防 SSRF**：`/api/proxy/<token>` 只放行 `*.bilivideo.com` / `*.bilivideo.cn` /
  `*.hdslb.com` 等 B 站自家域名，否则这个端点会变成访问内网与云元数据服务的跳板
- **直链不外泄**：页面里只有 base64url 编码的代理地址，上游签名留在服务端
- **短缓存**：音频直链带 `deadline` 签名，`Cache-Control: private, max-age=600`，
  避免缓存过久导致后续播放 403
- **限流**：解析接口按客户端 IP 限流（30 次/分钟），防止暴露在公网后被刷爆 B 站配额
- **访问令牌**：用 `-token` 开启，支持 `?token=`、Cookie 或 `X-Auth-Token` 三种方式；
  静态资源与 `/healthz` 不校验令牌（否则页面会没有样式）。
  页面会把已校验的令牌注入 `meta[name=biliaudio-token]`，前端据此**显式**把它加到
  解析请求与下载链接上——不能只依赖 Cookie，因为部分浏览器在 `http://` 或局域网
  地址、隐私模式下不会保存 Cookie，那样会出现「页面能看、一解析就 403」
- **透明传输**：Range/Content-Range 原样透传，手机浏览器可正常拖动进度条

## 油猴脚本（在 B 站页面直接下载）

`userscript/biliaudio.user.js` 把下载入口直接放到 B 站视频页的播放窗口下方，
不需要启动 `biliaudio serve`，也不需要手动复制视频链接。

1. 安装 [Tampermonkey](https://www.tampermonkey.net/) 或 Violentmonkey；
2. 新建脚本，粘贴 `userscript/biliaudio.user.js` 的内容并保存
   （也可以把文件拖进浏览器的扩展管理页）；
3. 打开 `https://www.bilibili.com/video/BV...`，播放窗口下方的
   `.video-toolbar-left` 工具栏里会出现一个下载图标按钮；悬停会显示
   「下载音频」提示；
4. 点击图标，脚本会自动读取当前视频/分 P，选择最高码率的 DASH 音频轨并下载。

实现要点：

- 视频 ID 和 cid 优先从 URL、`window.__INITIAL_STATE__` 读取；缺失时回退到
  `x/web-interface/view` 接口，因此从推荐位或列表页进入也能识别；
- 接口请求走页面自身的 `fetch`（Origin / Referer / Cookie 与播放器一致，
  不会因为扩展 Origin 触发 412）；
- 音频地址来自 `x/player/playurl`（`fnval=4048`，包含 DASH / Hi-Res / 杜比），
  接口失败时回退到播放器已经拿到的 `window.__playinfo__`；
- 自动挑选 `dash.audio` 中 `bandwidth` 最大的一档；没有 `dash.audio` 时回退到
  杜比/无损音轨，并按 MIME 决定 `.m4a` / `.flac` 等扩展名；
- 音频也通过页面自身的 `fetch` 流式下载（`credentials: 'omit'`，B 站 CDN
  的 CORS 为 `*`）；进度、成功、失败都会更新到按钮的 `title` 提示
  （图标同时脉冲/变色），直链失败时自动尝试 `backupUrl`；
- 按钮放在 `.video-toolbar-left-main`（`.video-toolbar-left` 内部），
  B 站重新渲染工具栏后会自动补回；
- 不会在 B 站 Vue 完成挂载/注水前往工具栏里插节点：脚本会先等根组件、
  顶栏和播放器 DOM 都就绪（并额外等待 500ms），避免破坏 hydration 导致
  顶栏等组件被卸载；
- 按钮只显示图标、不占文字宽度；下载/成功/失败状态通过 `title` 提示和
  图标颜色、脉冲动画表达，不额外插入文字；
- **站点仅限 B 站**：`@match` 只匹配 `www.bilibili.com` 的 `/video/*` 和
  `/list/*`，脚本内还会校验 `location.hostname` 必须是 `bilibili.com` 或其子域；
- 不申请 `GM_xmlhttpRequest` / `@connect` 跨域权限，所有网络请求都由当前
  B 站页面自身的 `fetch` 发出，因此**不会弹出跨域授权确认**；
- 番剧/课程等 PGC 页面使用另一套接口，不在脚本支持范围内；
- 高音质/会员音质取决于当前浏览器登录的 B 站账号权限。

## 目录结构

```
.
├── main.go                     # 入口，只做 os.Exit(cli.Run(...))
├── internal/
│   ├── cli/                    # 命令行：参数解析、终端输出、进度条、文件名模板
│   │   ├── run.go              #   下载模式
│   │   ├── serve.go            #   serve 子命令
│   │   ├── console.go
│   │   └── name.go
│   ├── web/                    # 网页版：路由、模板渲染、音频代理、限流
│   │   ├── server.go
│   │   ├── view.go
│   │   ├── proxy.go
│   │   ├── ratelimit.go
│   │   ├── log.go
│   │   └── assets/             #   模板 / CSS / JS / 图标（go:embed 内嵌）
│   ├── bili/                   # B 站接口：视频信息、分 P、DASH 音频直链、媒体白名单
│   │   ├── client.go
│   │   ├── id.go
│   │   └── media.go
│   ├── input/input.go          # 文本 → 视频 ID（含短链解析）
│   └── download/download.go    # 断点续传、重试、进度回调
├── userscript/
│   └── biliaudio.user.js       # 油猴脚本：在 B 站视频页直接下载音频
└── Makefile
```

## 开发

```bash
make test        # 单元测试
make test-race   # 带竞态检测
make vet         # 静态检查
make fmt         # 代码格式化
make run ARGS="BV1GJ411x7h7"
make serve       # 本地启动网页版
```

测试全部离线（用 `httptest` 假服务器驱动真实代码路径），覆盖：
ID 提取与大小写敏感性、分 P 展开与选择、文件名模板与清理、短链解析、
完整下载/续传/Range 被忽略/中途断流重试/超时取消、一次端到端的
「解析 → 选分 P → 下载」流程，以及网页版的首页/静态资源、表单回退与片段返回、
JSON 接口、音频 Range 透传、**SSRF 白名单拒绝**、访问令牌与限流。

## 说明

- 只取音频轨，不含视频。
- 高码率或部分受限视频需要登录态，可设置 `BILI_COOKIE`（浏览器 Cookie 串，
  或只填 `SESSDATA` 的值）。
- 仅供个人学习与试听，请勿用于商业用途。
