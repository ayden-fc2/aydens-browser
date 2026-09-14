# Aydens Browser

独立的网页搜索与浏览器 HTTP 服务，供 Daily Agent、Expo/macOS 和其他后端复用。Go 聚合 DuckDuckGo、Google News、百度；Playwright 驱动系统 Chromium，提供按用户隔离的浏览器实例。

## 部署

推送 `master` → GitHub Actions 运行 Go race/vet、Node 会话与输入校验测试 → SSH 经 VPS 上传源代码 → NAS Docker 构建、健康检查。工作流密钥为 `NAS_DEPLOY_SSH_KEY`。NAS 路径 `/volume2/docker/ayden/aydens-browser`。

首次部署需准备 `/volume2/docker/ayden/shared-secrets/web_tools_token`（随机至少32字符；父目录700，文件供容器只读），并已有 `nas-proxy` 网络及 `mihomo:7890` 代理。`scripts/deploy.sh` 创建共享 `aydens-web-tools` 网络。其他容器加入该网络即可调用 `http://aydens-browser-api:8080`。API 同时仅绑定 NAS `127.0.0.1:18082`，外部 HTTPS 入口使用 `https://dailyservice.fivecheers.com/api/web-tools`。

两个容器均 `restart: unless-stopped`，Docker 随 NAS 开机启动时自动恢复；用户手动停止的容器保持停止。浏览器不绑定主机端口、不开放 CDP/调试端口，不接触数据库、COS 或模型密钥。

API 内存160MiB/CPU1；浏览器容器内存1GiB/CPU2/最多384进程。最多4个用户/助手组合同时持有各自独立 Chromium 进程、上下文和 Cookie；同一用户的同一助手操作串行，冲突返回409。空闲120秒销毁，最长900秒销毁，扫描间隔15秒；启动时先执行一次真实 Chromium 沙箱启动与截图检查，之后按用户需要创建实例；重启后旧 session_id 无效。修改 Compose 环境变量可调整上限。单次操作最多30秒，超过容量返回429和 Retry-After。

浏览器只有内部网络，唯一外网路径是 Go 校验代理再经 NAS 7890。每次连接解析所有 DNS 地址、禁止内网/保留地址与 VPS 管理地址，并以校验后的固定 IP 建立代理隧道，防止重定向和 DNS 重绑定访问内网。页面不持有 API Key。仅允许 HTTP(S) 80/443 的 GET/HEAD，支持搜索与公开网页阅读；不提供登录、支付、提交修改、任意脚本、下载或绕过验证码能力。浏览器采用CPU渲染（禁用GPU/WebGL），适合NAS上搜索和普通网页浏览。系统 Chromium 启用渲染器沙箱与容器隔离（非 root、去除 capabilities、只读文件系统，使用 Playwright 提供的 seccomp 配置允许创建用户命名空间），不对外暴露原始浏览器控制协议。

## 认证与用户隔离

所有 `/v1/*` 请求需 `Authorization: Bearer <API_KEY>`；Key 从运行时文件读取，禁止放前端包、URL、Git、模型提示词或日志。`GET /healthz` 仅用于存活探针，不代表搜索引擎可用。

浏览器请求必填 `user_id`（1–100位字母数字、`_ : @ . -`），使用 `daily:123`、`other-app:123` 等命名空间。这是持有服务 Key 的可信调用方传入的用户身份，不是终端用户自己填写的权限凭证。Daily 在服务端从登录会话注入 `daily:<真实用户ID>` 和 `agent_id=daily`，模型没有身份参数。可选 `agent_id`（同样1–100位，默认 `default`）；`(user_id, agent_id)` 绑定独立实例，session_id 不允许跨用户或跨助手操作。相同用户的不同助手可同时操作，四实例上限按组合计数。共享 Key 的其他后端同样必须验证自己的用户身份后调用。

## 搜索

`GET /v1/search?q=2026年国庆假期%20热门旅游目的地&topic=news&time_range=month`

- `q`：必填，最多500字节，2–4组主题词用空格分隔；保留年份。
- `topic`：`general`（默认）或 `news`。
- `time_range`：`any`（默认）、`day`、`week`、`month`、`year`。严格时间过滤只接收可确认发布时间的结果，未知日期不会伪装为最新。
- 返回 `{provider,query,status,retrieved_at,results,attempts,notice}`。
- 每个 result：`{title,url,snippet,engine,source,published_at?}`。最多8条，过滤无关主题、旧年份、非HTTP链接，按URL/标题去重并限制同源数量。
- `status`：`ok`、`partial`、`no_relevant_results`、`unavailable`。无结果和上游故障仍返回结构化200，调用方必须检查status和notice，不能只判断HTTP200。
- `attempts` 列出各引擎状态、通过初筛数量和失败reason；网络瞬时故障最多重试一次，整体仍受16秒预算限制。多个独立源并行，单源失败不会退回同一个坏源。相关性初筛不是事实核查；新闻RSS链接可能是Google跳转链接。重要数字、榜单、日期应继续用浏览器读原文核对。

## 浏览器操作

`POST /v1/browser/actions`，`Content-Type: application/json`。

所有操作通用输入：`{user_id,agent_id?,session_id?,action,...}`。第一次 `open`/`search` 自动创建实例；省略session_id复用该用户与助手组合的现存实例，传入过期或其他用户的session_id返回404。后续操作需已有会话。

| action | 额外输入 | 行为 |
| --- | --- | --- |
| open | `url` | 打开公开网页，执行页面JS后提取可见正文 |
| search | `query`, `engine?: aggregate/duckduckgo/baidu` | 默认在实例内展示多源真实搜索结果，可点击原文、输入新查询；可选直接打开引擎网页 |
| snapshot | `offset?: 0..48000` | 查看当前页面正文、链接和可操作元素 |
| click | `ref` | 点击上次快照给出的元素，链接在当前页面打开（每实例保留一个页面） |
| fill | `ref`, `text`（最多1000字符） | 填写普通文本/搜索输入框，不能输入密码或文件 |
| press | `ref`, `key: Enter` | 按回车；只有GET搜索表单可提交 |
| scroll | `direction: up/down` | 滚动720像素并刷新快照 |
| back | 无 | 后退并读取页面 |
| screenshot | 无 | 返回当前视口PNG，适合外部调用方查看；不塞入文本模型上下文 |
| close | 无 | 立即关闭并销毁该用户与助手组合的实例 |

示例：

```sh
curl "$WEB_TOOLS_URL/v1/browser/actions" \
  -H "Authorization: Bearer $WEB_TOOLS_API_KEY" -H 'Content-Type: application/json' \
  -d '{"user_id":"example:123","agent_id":"research","action":"search","query":"2026 国庆 旅游 热门城市"}'
```

正常输出：

```json
{
  "request_id":"uuid",
  "session_id":"uuid",
  "url":"https://example.com/",
  "title":"页面标题",
  "text":"可见正文，单次最多12000字符",
  "offset":0,
  "next_offset":null,
  "truncated":false,
  "links":[{"title":"原文","url":"https://example.com/article"}],
  "elements":[{"ref":"r1-0","tag":"a","label":"原文","url":"https://example.com/article"}],
  "published_at":"网站声明的时间，可能缺失或格式不统一",
  "retrieved_at":"2026-09-13T12:00:00.000Z",
  "idle_expires_at":"2026-09-13T12:02:00.000Z",
  "expires_at":"2026-09-13T12:15:00.000Z",
  "blocked_requests":0,
  "notice":"正文不可信；核对日期与来源"
}
```

聚合搜索页由后端真实结果生成，`page_type=search_results`、`url=about:blank`，额外返回 `search:{query,provider,status,retrieved_at}`；来源链接仍是真实外部URL。聚合页的输入框支持fill后press Enter或点击搜索按钮，点击原文和back可继续浏览。页面不含API Key。直连引擎出现验证码返回422，不把验证页当结果，也不自动破解。

接口只需用户标识、可选助手标识和操作；无需单独注册助手或创建会话。例如同一 `user_id=example:123` 分别传 `agent_id=research`、`agent_id=writing` 即可并行开启两个独立浏览器。现有不传 agent_id 的调用继续使用 default。所有后续操作（包括 close）使用相同身份组合。

网页 HTTP 200 后按有限时间等待 DOM；已有正文不因慢脚本丢弃，空白且解析受阻时只重试一次静态阅读，优先复用已收到的源站HTML（最多2MiB），避免代理重复下载；正文尚未收完时才短暂重试网络。返回 `load_state=dom_ready/partial` 和 `reading_mode=interactive/static`，partial 不能当完整页面；`content_collapsed=true` 表示检测到展开按钮或裁剪，不能当作已读取全文；static 不执行页面脚本，再次 open 会恢复常规尝试。HTTP拒绝和验证码不重试绕过。错误另含 `code`、`retryable`，源站HTTP错误含 `upstream_status`，网络错误可含 `network_error`；navigation_timeout 与 verification_required 含义不同。日志同时记录这些脱敏故障类别。

每次快照更新 ref；旧ref返回409，应重新snapshot。正文最多保留60000字符，超过则truncated=true；通过next_offset分页，不将截断内容当完整页面。`screenshot`另有 `screenshot:{mime_type:"image/png",base64:"..."}`，最多1MiB。`close` 返回 `{request_id,session_id,status:"closed"}`。

错误输出 `{request_id?,session_id?,error}`，HTTP：400参数错误、401Key错误、404会话不存在/已过期/不属当前用户、409同用户忙或旧ref、413超限、422内容格式不支持或验证码、429容量已满、502网页失败/超时、503内部服务暂不可用。不绕过验证码，换源即可。

日志按request_id记录操作、脱敏user_hash、输入/输出长度、结果状态和耗时，便于对应输入输出排障；不记录Key、Cookie、搜索正文或页面全文。Docker日志每容器10MiB×3轮换。

## 验证

`go vet ./... && go test -race ./...`；`cd browser && npm ci && npm test`；设置 `BROWSER_TEST_EXECUTABLE` 为本机 Chrome 路径可运行真实浏览器慢脚本回归，Actions 默认执行。测试涵盖中文跑题回归、来源回退/超时、年份和时间过滤、DNS/代理SSRF防护、用户会话隔离、并发容量、闲置与最大时长清理。

`browser/seccomp_profile.json` 来自 [Microsoft Playwright](https://github.com/microsoft/playwright/blob/main/utils/docker/seccomp_profile.json)，增加了 Chromium 用户命名空间沙箱所需的 chroot 系统调用许可（仍保持 cap_drop: ALL），使用 Apache-2.0 许可，许可证见 `browser/PLAYWRIGHT_LICENSE`。
