# one-search-api

自托管、与 **DeepSeek Search / Fetch API 格式兼容**的搜索与抓取服务。
用 Go 写成单二进制，后端聚合免费搜索源（SearXNG / Bing 国内版与国际版 / DuckDuckGo / Brave / Tavily），
对外暴露和 DeepSeek 一样的两个接口：

```
POST /v1/tools/search   → {"results":[{rerank_score,title,url,snippet,summary,content,site_name,site_icon,date,authority}]}
POST /v1/tools/fetch    → {"title":"...","content":"# Markdown 正文"}
```

> 适用场景：自己写的 agent / RAG / 客户端，把 `base_url` 从 `https://api.deepseek.com` 换成本服务即可。
> 定位是 B 形态（自己 + 少数朋友）：不做计费，但有 API Key、配额、并发闸门与反滥用。

---

## 为什么做这个

- DeepSeek 的 Search / Fetch 按 ¥5 / 1000 次计费（成功请求才计费），且在 DSH / Claude Code / Codex 中的
  server 端调用从 **2026-11-01 00:00（北京时间）** 起开始收费。
- 官方接口只有 5 QPS 的账号级限制，且必须用 DeepSeek 的 Key。
- 自托管后：不按次计费、可接入任意免费源、可给任意模型/客户端复用同一套 tool schema、可加自己的数据源。

**先说清楚**：`¥5/1000` 本身很便宜，日均几百次搜索只有几十块/月。这个项目的真正价值是
「不依赖官方额度 + 可自控 + 可复用」，不是「省大钱」。

---

## 特性

| 能力 | 说明 |
|---|---|
| 格式兼容 | 两个端点的请求/响应形状、可空字段语义、OpenAI 风格错误信封 |
| 多源聚合 | SearXNG、Bing（国内/国际 × RSS/HTML）、DuckDuckGo、Brave、Tavily，可单启用/加权/限速 |
| 检索管线 | URL 归一化 → 去重 → RRF 融合 → 域名多样性 → 启发式打分 → 字段富化 |
| 内容抓取 | readability + HTML→Markdown、charset 嗅探、缓存、`need_content` 并发补正文 |
| 鉴权与配额 | 每 Key：RPM 令牌桶、每日配额、provider 白名单、最大返回条数 |
| 反滥用 | 请求体上限、字段长度校验、全局并发闸门、provider 熔断、每 provider 限速 |
| SSRF 防护 | 私网/回环/链路本地/CGNAT 拦截、预解析 IP 直连（防 DNS rebinding）、重定向逐跳复检、端口白名单 |
| 可观测 | slog 结构化日志、`/healthz`、Prometheus `/metrics` |
| 部署 | 单二进制；附 Dockerfile 与 docker-compose（含 SearXNG） |

---

## 快速开始（不需要 SearXNG）

默认配置只启用 **Bing RSS**，开箱即可用：

```bash
go build -o onesearch ./cmd/onesearch

# Linux/macOS
./onesearch                       # 监听 :8080，auth.mode=none（仅本机调试用）

# Windows PowerShell
.\onesearch.exe
```

```bash
curl -s http://127.0.0.1:8080/v1/tools/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"DeepSeek V4 发布","count":3}'
```

```powershell
# Windows PowerShell
$body = [Text.Encoding]::UTF8.GetBytes('{"query":"DeepSeek V4 发布","count":3}')
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/v1/tools/search `
  -ContentType 'application/json; charset=utf-8' -Body $body
```

实测输出（Bing RSS，耗时约 0.25s）：

```json
{
  "results": [
    {
      "title": "deepseek-ai/DeepSeek-V4-Pro-0813 · Hugging Face",
      "site_name": "huggingface.co",
      "site_icon": "https://huggingface.co/favicon.ico",
      "authority": -1,
      "rerank_score": 2.77,
      "date": "2026-10-16T14:38:00+00:00",
      "url": "https://huggingface.co/deepseek-ai/DeepSeek-V4-Pro-0813",
      "summary": "...",
      "snippet": "...",
      "content": ""
    }
  ]
}
```

> 字段顺序与官方一致（`title, site_name, site_icon, authority, rerank_score, date, url,
> summary, snippet, content`）。

> **Bing 渠道说明**：RSS 与 HTML 是同一个搜索后端的两种渲染，**同一 query 的结果集完全一致**，
> 不存在「RSS 质量差」。真正的坑是 Bing 偶发返回 `200 + 空 channel`（软限流），或者
> **串回别的 query 的缓存结果**。本服务对这两种情况分别用「同渠道重试 + HTML 回退」和
> 「批次相关性共识过滤」兜底，详见下方 [Bing 渠道与指纹](#bing-渠道与指纹国内版--国际版)。

---

## 推荐部署（Docker Compose + SearXNG）

```bash
docker compose up -d --build
```

compose 会启动：

- `searxng`：内置 `deploy/searxng/settings.yml`，已开启 `json` 输出格式，仅监听 `127.0.0.1:8888`。
- `onesearch`：监听 `127.0.0.1:8080`，通过 `ONESEARCH_SEARXNG_URL=http://searxng:8080` 接入。

访问方式：

```bash
curl -s http://127.0.0.1:8080/v1/tools/search \
  -H 'Authorization: Bearer sk-change-me-please' \
  -H 'Content-Type: application/json' \
  -d '{"query":"golang context 取消","count":5}'
```

默认 Key 是 `sk-change-me-please`，**上线前务必改掉**（见下方配置说明）。

---

## 配置

完整示例见 [`config.example.yaml`](config.example.yaml)。优先级：**默认值 < YAML 文件 < 环境变量**。

```bash
./onesearch -config config.yaml
```

### 最小配置（多 Key + 配额）

```yaml
auth:
  mode: key
  keys:
    - name: me
      token: "sk-onesearch-change-me"
      rpm: 120          # 每分钟请求数
      burst: 20         # 令牌桶容量
      daily_quota: 5000 # 每天请求数（0 = 不限）
      max_results: 50
      allowed_providers: []   # 空 = 允许全部
    - name: friend
      token: "sk-onesearch-friend"
      rpm: 60
      daily_quota: 1500
      max_results: 20
      allowed_providers: [searxng, bing]
```

`token` 可以写明文，也可以写 `sha256:<hex>`（推荐，用 `echo -n 'sk-xxx' | sha256sum` 生成）。
明文在进程启动时即哈希，运行期只做常数时间比较。

### 环境变量覆盖

| 变量 | 作用 |
|---|---|
| `ONESEARCH_ADDR` | 监听地址，如 `:8080` |
| `ONESEARCH_AUTH_MODE` | `none` / `key` |
| `ONESEARCH_AUTH_KEYS` | `name:token[:rpm[:daily_quota[:max_results]]]`，逗号分隔多个 |
| `ONESEARCH_SEARXNG_URL` | SearXNG 地址；设置后自动启用该 provider |
| `ONESEARCH_LOG_LEVEL` / `ONESEARCH_LOG_FORMAT` | 日志级别 / `json` 或 `text` |

### Provider 一览

| type | 需要 Key | 说明 |
|---|---|---|
| `searxng` | 否（可选 token） | **推荐主源**；需在 SearXNG `settings.yml` 的 `search.formats` 中加 `json`；支持 `pageno` 翻页，`count>20` 靠它和 Brave 补满 |
| `bingrss` | 否 | Bing 国内/国际版 RSS；约 5 KB，`date` 字段最全，**默认渠道**；空 channel 自动重试，失败回退 HTML；每页固定 10 条，`count>10` 时自动尝试 `&first=` 翻页（无新增即停） |
| `binghtml` | 否 | Bing 国内/国际版 HTML；约 100 KB，摘要结构更全；同样支持自适应 `&first=` 翻页 |
| `duckduckgo` | 否 | 解析 HTML；可能触发人机验证；**中国大陆网络通常不可达** |
| `brave` | 是 | 免费 2000 次/月，质量好 |
| `tavily` | 是 | 免费 1000 次/月，自带摘要能力 |

### Bing 渠道与指纹（国内版 / 国际版）

Bing 的 RSS 与 HTML 共用一套搜索后端，区别只是渲染方式：

| 渠道 | 体积 | 特点 |
|---|---|---|
| `bingrss` | ~5 KB | 自带 `pubDate`，`date` 字段更完整；默认 |
| `binghtml` | ~100 KB | `.b_caption` 摘要、`.news_dt` 日期，结构更全 |

「4 个渠道」= 2 种类型 × 2 个市场，用 4 个 provider 实例表达：

```yaml
- {name: bing,           type: bingrss,  base_url: https://cn.bing.com,  market: zh-CN}
- {name: bing-html,      type: binghtml, base_url: https://cn.bing.com,  market: zh-CN}
- {name: bing-intl,      type: bingrss,  base_url: https://www.bing.com, market: en-WW}
- {name: bing-intl-html, type: binghtml, base_url: https://www.bing.com, market: en-WW}
```

> `cn.bing.com` 在中国大陆以外会 **301 到 `www.bing.com`**（并改写路径），所以境外部署请直接配
> `www.bing.com`；反之在国内请用 `cn.bing.com`。

**自动反爬（默认开启，无需配置）**

- 每个 provider 独立会话：**启动时后台预热**（失效后自动重预热）访问首页，拿到 `MUID / _EDGE_S / SRCHD / SRCHUSR / SRCHUID / _SS` 等真实 cookie；
- 每次会话**随机生成**浏览器指纹：UA（Windows / macOS / Linux 随机 + Chrome 主版本）、`sec-ch-ua`、平台、Accept-Language、32 位大写 `cvid`；
- 搜索 URL 带真实参数：`form=QBLH&sp=-1&lq=0&pq=<上次查询>&sc=<生成值>&qs=n&sk=&cvid=…&ghsh=0&ghacc=0&ghpl=`，以及 `setmkt=`、`Referer`、`Sec-Fetch-*`；
- `mobile: true` 切移动端指纹：移动 UA + `PC=EMMX01&form=LBT003&scope=web&darkschemeovr=1`。实测移动端 HTML 每页只有 5 条，所以默认桌面；RSS 不受影响；
- 空结果、出错或结果离题时**轮换指纹与 cookie** 并同渠道重试，仍失败则 RSS 回退到 HTML 解析；
- 结果整批离题（Bing 回放别的 query 缓存）会被 **批次相关性过滤** 丢弃，不污染最终排序。

可手动覆盖：`user_agent` / `referer` / `cookies` / `mobile` / `warmup: false` / `extra_params`（任意附加参数）。

---

## API

### `POST /v1/tools/search`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `query` | string | 是 | 1～128 字符；支持 `site:`、`-site:`、`"短语"`、`-词`、`lang:`、`after:`、`before:` |
| `count` | int | 否 | 1～50，默认 10 |
| `ori_question` | string/null | 否 | ≤128 字符；v1 仅记录，不参与排序 |
| `localization` | string | 否 | 接收但忽略（无 GeoIP） |
| `need_content` | bool | 否 | 为 true 时对全部结果抓取正文填入 `content`（默认上限 50，与官方一致） |

请求必须带 `Content-Type: application/json`，body 必须是 JSON 对象；鉴权先于参数校验
（与官方一致：Key 不对时即使 body 非法也返回 401）。

响应固定 10 个字段，字段顺序与官方一致；`results` 恒为数组（无结果 `[]`）。可空字段输出 `null`，
`date` 为 `+00:00` 偏移的 UTC 时间。

### `POST /v1/tools/fetch`

```json
{"url": "https://example.com/article"}
```

返回 `{"content": "# Markdown 正文", "title": "标题"}`。与官方一致：HTML 之外的
`text/plain`、`text/markdown`、`text/css`、`text/javascript`、`application/json` 也会原样返回；
XML / CSV / PDF / 图片等返回 404。

### 错误码

| HTTP | type | code | 触发 |
|---|---|---|---|
| 400 | invalid_request_error | invalid_request_error | JSON/字段非法、query 全空白、count 越界、URL 非法、Content-Type 不对 |
| 401 | authentication_error | invalid_request_error | Bearer 缺失/错误（文案与官方一致） |
| 402 | insufficient_quota | insufficient_quota | 每日配额耗尽（自托管扩展） |
| 404 | not_found_error | invalid_request_error | fetch 一切失败：无正文 / 上游不可用 / 媒体类型不支持 / SSRF 拦截 |
| 405 | —（空响应体） | — | 方法不对，响应头 `Allow: POST` |
| 413 | payload_too_large_error | invalid_request_error | 请求体或页面超限 |
| 429 | rate_limit_error | rate_limit_exceeded | RPM 超限 / 全局并发满 |
| 500 / 504 | server_error / gateway_timeout_error | 同 type | 内部错误 / 上游超时 |

信封与官方一致：`{"error":{"message","type","param":null,"code"}}`，message 末尾带
`(request_id: …)`；429/402 带 `Retry-After`，所有响应带 `X-Request-Id` 与 CORS 头。

---

## 与官方实现的差异（务必先读）

官方契约已用真实 API Key 逐项实测对照，完整证据见 **[docs/COMPAT.md](docs/COMPAT.md)**；
`internal/compat/official_test.go` 把这些事实固化成了可重跑测试。仍然保留的差异：

1. **分数和摘要不可比。** `rerank_score` 是 RRF + 权威性 + 时效性的启发式映射；
   `authority` 是域名分级表；`summary` 是正文截断或摘要拼接。字段形状与取值范围一致，
   但如果你按官方语义做阈值（如 `score>=1.5`、`authority>=3`），结果分布会不同。
2. **`after:` / `before:` 是后置过滤。** 只在结果自带日期时生效，日期未知的结果会保留。
3. **`localization` 不生效**（没有 GeoIP 库）。
4. **`ori_question` 不参与排序**（v1 无 LLM 富化）。
5. **provider 白名单是过滤，不是报错**：过滤后没有可用源时返回 `{"results":[]}`。
6. **`need_content` 尽力而为**：默认对全部结果抓正文（上限 50），被反爬拦截/超时的结果 `content` 为 `""`（官方也会跳过抓不到的站）。
7. **乱码查询可能返回空数组**：有离题结果过滤；官方会返回 10 条低分结果。
8. **`site_icon` 用自有模板**（默认 `<host>/favicon.ico`）；官方统一用
   `https://cdn.deepseek.com/site-icons/<domain>`，可用 `site_icon_template` 改成后者。
9. **不支持 JS 渲染**：SPA 页面抓取失败率高于官方。

---

## 运维

- 健康检查：`GET /healthz`（返回各 provider 熔断状态，无鉴权）
- 指标：`GET /metrics`（Prometheus 文本，无鉴权，**不要直接暴露到公网**）
- 日志：默认 JSON，含 `request_id`、`key`、`endpoint`、`status`、`duration_ms`、`results`
- 缓存：搜索 5 分钟、抓取 30 分钟、TTL-LRU + singleflight；重启后配额与缓存清零

常用指标：

```
onesearch_requests_total{endpoint,status}
onesearch_request_duration_seconds{endpoint}
onesearch_provider_requests_total{provider,outcome}  # ok/error/hedged/stale_dropped/rate_limited/breaker_open
onesearch_provider_breaker_state{provider}           # 由 /healthz 暴露
onesearch_fetch_total{status}
onesearch_cache_hits_total{kind}
onesearch_rate_limited_total{reason}
onesearch_results_filtered_total                     # 离题结果被过滤的条数
```

---

## 安全

公网部署前至少做到：

1. `auth.mode: key`，并替换掉示例 Key；`allowed_providers` 按需收紧。
2. 前置 HTTPS 反代（Caddy/Nginx），只暴露 `/v1/`、`/healthz`；`/metrics` 走内网。
3. `fetch.allow_private` 必须保持 `false`（默认）；确需抓内网时单独起一个实例。
4. 配置 `server.trusted_proxies`，否则 `X-Forwarded-For` 不被采信（防伪造绕过按 IP 限流）。
5. 控制 `search.max_concurrency` 与各 provider 的 `rpm`，避免把上游打挂。

已知合规注意：

- **Bing**：微软对 RSS 结果的版权声明写明「仅限个人非商业用途在 RSS 聚合器中呈现」；HTML/RSS
  抓取也会受其反爬与使用条款约束。自用风险较低，**公开托管或商业化需自行评估合规**。
- 本服务只做「像真实浏览器一样请求 + 失败重试」，不破解验证码、不绕过付费墙；被挑战时降级返回。
- **DuckDuckGo** 解析 HTML 可能违反其 ToS；访问量大了会被验证码拦截。
- **SearXNG** 本身是 AGPL 软件，自托管没问题，但它也可能被上游引擎封 IP。

---

## 开发

```bash
go vet ./...
go test ./...
go test -race ./...      # 建议（Linux/WSL）
go build -o onesearch ./cmd/onesearch

# 真实网络冒烟（可选）
go test ./internal/provider -tags live -run TestLiveBingCN -v -count=1
ONESEARCH_LIVE_INTL=1 go test ./internal/provider -tags live -run TestLiveBingIntl -v -count=1  # 需在境外机器执行

# 官方契约对照（可选，会消耗少量官方额度；事实清单见 docs/COMPAT.md §8）
DEEPSEEK_API_KEY=sk-... go test ./internal/compat -tags official -v -count=1
```

项目结构：

```
cmd/onesearch/            入口
internal/api/             路由、中间件、两个 handler、/healthz、/metrics
internal/auth/            Key 鉴权、RPM、每日配额
internal/cache/           TTL-LRU + singleflight
internal/compat/          官方接口对照 golden 测试（-tags official）
internal/config/          YAML + 环境变量 + 校验
internal/enrich/          snippet/summary/site/date 富化
internal/extract/         HTML → 标题 / 正文 / Markdown
internal/fetchguard/      SSRF 校验与安全 Dialer
internal/fetchsvc/        抓取编排与缓存
internal/httpx/           线协议类型与错误信封
internal/limit/           令牌桶、信号量、熔断器
internal/obs/             日志与指标
internal/provider/        搜索源实现（Bing 会话/指纹、SearXNG、DDG、Brave、Tavily）
internal/query/           搜索语法解析
internal/rank/            归一化、去重、RRF 融合、打分
internal/search/          并发扇出、hedge、正文补齐
internal/siteinfo/        域名 → authority / 站点名 / favicon
docs/DESIGN.md            设计文档（契约、字段映射、验收标准）
```

---

## 已知限制

- Bing 会偶发 `200 + 空 channel` 软限流和「回放别的 query 缓存」；已用同渠道重试、HTML 回退、批次相关性过滤兜底，极端情况下仍可能少几条结果。
- `cn.bing.com` 在中国大陆以外会 301 到国际版；境外部署请配 `www.bing.com`。
- DDG 在部分网络不可达；SearXNG 依赖自建实例。
- 无 JS 渲染；无 LLM 摘要/语义重排；无 GeoIP。
- 配额与缓存驻留内存，重启清零；多实例部署时各实例独立计数。
- 不保证跟随 DeepSeek 未来 schema 变更。

## Roadmap（未实现）

- LLM 富化（summary 重写 / 语义重排），配置开关
- Redis 共享缓存与配额
- 管理 API：Key 动态增删、用量查询
- 更多源：Wikipedia、GitHub、arXiv、Hacker News
- 抓取重试与 JS 渲染兜底

## 参考与同类项目

- [DeepSeek 网页搜索与抓取文档](https://api-docs.deepseek.com/zh-cn/guides/search)
- [SearXNG](https://github.com/searxng/searxng) 与其 [Search API](https://docs.searxng.org/dev/search_api.html)
- [yokingma/one-search-mcp](https://github.com/yokingma/one-search-mcp)：多源搜索 MCP（TS）
- [fastcrw/crw](https://github.com/fastcrw/crw)：Firecrawl/Tavily 兼容的自托管抓取服务（Rust）
- [Ghilteras/searxng-gateway](https://github.com/Ghilteras/searxng-gateway)：SearXNG 决策代理 + 熔断（Go）
- [digitalninjanv/free-web-search](https://github.com/digitalninjanv/free-web-search)：免费搜索后端链与 SSRF 防护（Python）

## License

MIT
