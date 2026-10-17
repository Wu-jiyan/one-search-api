# One Search API — 设计文档（v1）

自托管、与 DeepSeek **Search / Fetch API** 请求与响应格式兼容的搜索抓取后端。
用 Go 实现，单二进制，免费搜索源聚合（SearXNG / Bing RSS / DuckDuckGo / Brave / Tavily）。

定位（B 形态）：**自己用 + 少数朋友用**。不做计费、不做多租户数据库，但必须有身份、配额、
并发闸门和反滥用；配额与限流状态全部驻留内存，进程重启即清零（明确接受）。

---

## 1. 目标与非目标

### 目标

1. `POST /v1/tools/search`、`POST /v1/tools/fetch` 与 DeepSeek 官方接口的**请求/响应形状兼容**，
   客户端只需把 base_url 换成本服务。
2. 免费源聚合：SearXNG、Bing RSS、DuckDuckGo HTML、Brave、Tavily 可单独启用/加权。
3. 每个 API Key 独立配额（RPM + 每日次数）、可选 provider 白名单、最大返回条数。
4. 抗滥用：请求体大小、字段长度、URL 校验、SSRF 防护、并发上限、上游熔断。
5. 可观测：结构化日志、`/healthz`、Prometheus 文本指标。
6. 单二进制 + 可选 Docker Compose（含 SearXNG）。

### 非目标（v1 明确不做）

- 计费、余额、充值、发票。
- 持久化配额/审计（重启清零）。
- 向量检索、答案生成、多轮会话。
- JS 渲染页面抓取（不做 headless browser）。
- 与 DeepSeek **分数语义**等价（见 §7「保真度声明」）。
- `localization` 的地理定位（接收但忽略）。

---

## 2. 兼容性契约

Base: `http://<host>:<port>`，两个端点均为 `POST`、`application/json`、`Authorization: Bearer <key>`。

### 2.1 Search 请求

```json
{
  "query": "DeepSeek V4 发布 site:github.com",
  "count": 5,
  "ori_question": "DeepSeek V4 什么时候发布的？",
  "localization": "",
  "need_content": true
}
```

| 字段 | 类型 | 必填 | 规则 |
|---|---|---|---|
| `query` | string | 是 | 1～128 个 Unicode 字符，不能全空白；支持搜索语法（§2.4） |
| `count` | int | 否 | 1～50，默认 10；超过 `search.max_count` 按 `max_count` 截断（服务器配置） |
| `ori_question` | string / null | 否 | ≤128 字符；v1 仅记录日志与指标，不参与排序 |
| `localization` | string | 否 | 接收但忽略（无 GeoIP） |
| `need_content` | bool | 否 | 为 true 时对前 N 条结果抓取正文填入 `content` |

未知字段**忽略**（与官方一致，不返回 400）。

### 2.2 Search 响应

```json
{
  "results": [
    {
      "rerank_score": 2.41,
      "title": "...",
      "url": "https://...",
      "snippet": "...",
      "summary": "...",
      "content": "",
      "site_name": "GitHub",
      "site_icon": "https://github.com/favicon.ico",
      "date": "2026-10-01T00:00:00Z",
      "authority": 3
    }
  ]
}
```

- `results` **始终存在**，无结果时为 `[]`（不是 null）。
- 10 个字段**始终出现**；未知的可空字段输出 `null`，字符串字段输出 `""`。
- `rerank_score` ∈ [0,3]，保留两位小数。
- `authority` ∈ [-1,4]，`-1` 表示未知。
- `date` 为 UTC RFC3339 或 `null`。

### 2.3 Fetch

请求 `{"url": "https://example.com/a"}`：
`url` 必填、≤8192 字符、必须是有 host 的 http/https、不能带 userinfo。

响应 `{"title": "...", "content": "# Markdown 正文"}`，两字段始终存在。
抽取不到正文时返回 404（与官方语义一致）。

### 2.4 搜索语法

| 语法 | 支持方式 |
|---|---|
| `site:` / `-site:` | 解析后重写进各引擎查询；SearXNG/Bing/DDG/Brave 原生支持；Tavily 无 → 后置过滤 |
| `"短语"` / `-"短语"` | 同上 |
| `-term` | 同上 |
| `lang:zh,en` | 转 `language` 参数（SearXNG/DDG/Brave）；不支持者忽略 |
| `after:` / `before:` | 能透传的透传（Bing `filters` 不支持则不发）；**统一后置过滤**：已知日期的结果越界即丢弃，日期未知的结果保留 |

后置过滤是本服务与官方行为的主要偏差之一：完全依赖结果自带的日期字段。

### 2.5 错误信封

OpenAI 兼容形状，`param` 恒为 `null`：

```json
{"error": {"message": "...", "type": "invalid_request_error", "param": null, "code": "invalid_request_error"}}
```

| HTTP | type | code | 触发 |
|---|---|---|---|
| 400 | invalid_request_error | invalid_request_error | JSON 错误、字段非法、query 全空白 |
| 401 | authentication_error | invalid_api_key | Bearer 缺失/无效 |
| 402 | insufficient_quota | insufficient_quota | 每日配额耗尽 |
| 403 | permission_error | permission_error | SSRF / 抓取安全策略拒绝 |
| 404 | not_found_error | not_found | Fetch 无可用正文 |
| 405 | invalid_request_error | method_not_allowed | 端点存在但方法不对（扩展） |
| 408 | timeout_error | request_timeout | 客户端请求体读取超时 |
| 413 | invalid_request_error | payload_too_large | 请求体 / 抓取页面超过上限 |
| 429 | rate_limit_error | rate_limit_exceeded | RPM 超限或全局并发满 |
| 500 | server_error | internal_error | 未预期错误 |
| 504 | timeout_error | upstream_timeout | 所有上游超时 |

官方只文档化了 `invalid_request_error`；上表其余 code 是本服务的扩展，信封形状保持兼容。
429/402 响应带 `Retry-After`（秒）与 `X-Request-Id`。

---

## 3. 架构

```
cmd/onesearch/main.go            启动：配置 → 依赖装配 → HTTP → 优雅退出
internal/config/                 YAML + 环境变量 + 默认值 + 校验
internal/httpx/                  请求/响应结构体、错误信封、JSON 写出
internal/query/                  搜索语法解析与引擎查询重建
internal/provider/               Provider 接口 + 5 个实现 + 共享 HTTP 客户端
internal/fetchguard/             SSRF 校验（URL / IP / 重定向 / 自定义 Dialer）
internal/extract/                HTML → title/正文/Markdown
internal/cache/                  TTL-LRU + singleflight
internal/rank/                   URL 归一化、去重、RRF 融合、打分
internal/siteinfo/               域名 → authority / 站点名 / 图标
internal/enrich/                 字段富化（date/authority/site/snippet/summary/icon）
internal/search/                 检索编排（扇出、超时、正文填充）
internal/fetchsvc/               抓取编排（缓存、抽取、错误映射）
internal/auth/                   Key 鉴权、配额、RPM 记账
internal/limit/                  令牌桶、信号量、熔断器
internal/obs/                    日志、指标注册表
internal/api/                    路由、中间件、handler、健康检查、/metrics
```

数据流（search）：

```
HTTP → 中间件(恢复/日志/体积/鉴权/配额/RPM/超时)
     → 解析 query 语法
     → 并发扇出到所有启用 provider（各自超时 + 熔断 + 限速）
     → 归一化 URL、去重、RRF 融合、后置日期过滤
     → 富化（authority/site/date/snippet/summary/icon）
     → need_content: 并发抓取正文 + 缓存
     → 截断 count → 输出
```

---

## 4. Provider 接口

```go
type Provider interface {
    Name() string
    Search(ctx context.Context, q query.Query) ([]Result, error)
}
```

所有实现共用：
- 共享 `http.Client`（连接复用、HTTP/2、UA、代理可选）。
- 单 provider 超时（默认 2.5s），失败不阻塞整体，结果集允许部分为空。
- 每个 provider 独立令牌桶（RPM）+ 熔断器（连续失败 N 次打开，冷却后半开）。

| type | 端点 | 鉴权 | 产出 | 备注 |
|---|---|---|---|---|
| `searxng` | `GET {base}/search?format=json` | 可选 `X-Token` | title/url/content/publishedDate/score/favicon | 需在 SearXNG settings.yml 开启 `json` 格式 |
| `bingrss` | `GET {base}/search?format=rss` | 无 | title/link/description/pubDate | 官方声明仅限个人非商业用途；默认 base `https://cn.bing.com` |
| `duckduckgo` | `POST {base}/html/` | 无 | title/url/snippet | 解析 `result__a` / `result__snippet`；易触发验证码，默认权重低 |
| `brave` | `GET https://api.search.brave.com/res/v1/web/search` | `X-Subscription-Token` | title/url/description/age/page_age | 免费 2000 次/月，需自备 key |
| `tavily` | `POST https://api.tavily.com/search` | `Authorization: Bearer` | title/url/content/published_date | 免费 1000 次/月，需自备 key |

新增 provider：实现接口 + 在 `provider.New` 注册 type 即可，不改编排层。

---

## 5. 检索管线细节

1. **扇出**：并发调用所有启用 provider，带整体 deadline（默认 12s）。任一 provider 超时只丢弃该源。
2. **Hedge（防慢源拖垮延迟）**：当已收到的结果数达到 `max(1, min(5, count/2))` 时启动 `hedge_delay`
   （默认 800ms）计时器；计时器到期即取消剩余未返回的 provider。被 hedge 取消的 provider 记为
   `outcome="hedged"`，不计熔断失败。这也意味着结果可能少一两个来源的贡献，是延迟与召回率的显式取舍。
3. **URL 归一化**：小写 host、去 fragment、去跟踪参数（`utm_*`/`fbclid`/`gclid`/`spm`/`ref`）、
   去尾部 `/`、解包已知跳转（Google `/url?q=`、Bing `/ck/a`、DDG `/l/?uddg=`）。
4. **去重**：同 canonical URL 合并；同 host + 归一化标题相同合并；保留先出现者并合并来源与摘要。
5. **融合**：RRF `score += weight(engine) / (60 + rank)`；再叠加标题命中、精确短语、`site:` 命中、
   时效衰减、authority 先验，映射到 [0,3]（见 §6）。
6. **多样性**：同一注册域最多 `search.per_domain_limit`（默认 3）条，超出的延后。
7. **截断**：排序后取 `count` 条；`count` 上限受 Key 的 `max_results` 与服务器 `search.max_count` 双重约束。

---

## 6. 各字段富化规则

| 字段 | 来源 | 规则 |
|---|---|---|
| `title` | 引擎 | 去 HTML 标签、合并空白、截断 300 字符 |
| `url` | 引擎 | 归一化后的绝对 URL |
| `snippet` | 引擎 content/description | 去标签、反转义、合并空白，截 200 字符（优先 100～200） |
| `summary` | `content` 或组内 snippets | 有正文：取正文前 800 字符内最后一句边界（目标 600～800）；无正文：拼接组内 snippet 至约 600 字符；再退化为 snippet |
| `content` | 抓取 | `need_content=false` 恒 `""`；true 时为 Markdown，失败为空串 |
| `site_name` | 域名映射表 → 页面 `og:site_name` → 注册域 | 内置约 120 条常见站点映射，未知回退注册域（如 `example.com`） |
| `site_icon` | 模板 | 默认 `https://{host}/favicon.ico`，可配置模板 |
| `date` | RSS pubDate / SearXNG publishedDate / Tavily published_date / 页面 meta | 多格式解析后统一 UTC RFC3339；失败为 `null` |
| `authority` | 域名分级表 | 见下 |
| `rerank_score` | 融合分 | 见下 |

### authority 分级

| 分值 | 判定 |
|---|---|
| 4 | `.gov` / `.gov.cn` / `.edu` / `.edu.cn` / `.mil` / `.int`、WHO/UN/EU 等 |
| 3 | 官方文档与源码站（github.com、go.dev、python.org、developer.mozilla.org…）、顶级期刊与通讯社（nature.com、science.org、reuters.com、apnews.com…）、wikipedia |
| 2 | 主流技术媒体与厂商（36kr、infoq、sspai、cloudflare.com、aws.amazon.com…） |
| 1 | 明确 UGC/论坛（reddit、知乎、V2EX、CSDN、掘金、贴吧、微博…） |
| 0 | 明确低质/聚合垃圾域（内置小名单） |
| -1 | 未收录（默认） |

### rerank_score

```
rel    = normRRF ^ 0.6                        // normRRF = rrf / maxRRF，∈[0,1]
authN  = authority >= 0 ? authority/4 : 0.5   // 未知按中性
fresh  = date ? exp(-ageDays/180) : 0.5
raw    = 0.62*rel + 0.23*authN + 0.15*fresh
boost  = min(0.25, 0.06*titleTermHits + 0.10*phraseHit + 0.08*siteHit)
score  = clamp(3*(raw+boost), 0, 3)
```

### 保真度声明（写进 README）

`rerank_score` / `authority` / `summary` 是**启发式产出**，只保证字段形状与取值范围，
不代表与 DeepSeek 官方分数可比。客户端若按 `score>=1.5`、`authority>=3` 做阈值判断，
得到的结果分布会与官方不同。

---

## 7. Fetch 管线与安全

1. **URL 校验**：长度 ≤8192、scheme ∈ {http,https}、必须有 host、禁止 userinfo、端口白名单（80/443/8080/8443，可配）。
2. **SSRF 防护**：
   - 主机名 denylist：`localhost`、`*.local`、`*.internal`、`metadata.google.internal` 等。
   - 解析后的每个 IP 都校验：loopback、私网（10/8、172.16/12、192.168/16）、CGNAT 100.64/10、
     link-local 169.254/16、组播、未指定 0.0.0.0、IPv6 ULA fc00::/7、IPv4-mapped IPv6。
   - **自定义 Dialer 用已校验的 IP 直连**，避免 DNS rebinding。
   - 重定向逐跳复检（≤5 跳），跨协议降级拒绝。
   - `fetch.allow_private=true` 可关闭（仅本地调试），默认 false。
3. **读取限制**：超时 10s、响应体 ≤5MB、仅接受 `text/html`/`application/xhtml+xml`/`text/plain`。
4. **抽取**：charset 嗅探 → readability 找正文 → HTML 转 Markdown；标题取 readability/`<title>`。
   正文少于 `fetch.min_content_runes`（默认 120）判为无正文 → 404。
5. **缓存**：按归一化 URL 缓存成功结果，TTL 30min，singleflight 合并并发请求。
6. 失败映射：SSRF/策略 → 403；无正文 → 404；超时 → 504；页面过大 → 413。

---

## 8. 鉴权 / 配额 / 限流 / 反滥用

| 机制 | 粒度 | 默认 | 超限行为 |
|---|---|---|---|
| Bearer Key | 请求 | `auth.mode: key` | 401 |
| RPM | 每 Key（匿名按 IP） | 配置，默认 60 | 429 + `Retry-After` |
| 每日配额 | 每 Key（匿名按 IP） | 配置，默认 2000 | 402 |
| 最大条数 | 每 Key | 配置，默认 50 | 静默截断 |
| Provider 白名单 | 每 Key | 空=全部 | 过滤该 Key 不可用的源（不报错；全被过滤则返回空数组） |
| 全局并发 | 进程 | 默认 32 | 429 |
| 单请求超时 | 请求 | 默认 30s | 504 |
| 请求体 | 请求 | 1MiB | 413 |
| Provider 级 RPM | 每 provider | 配置 | 内部排队/丢弃该源 |

- Key 支持明文或 `sha256:<hex>`；明文在加载时即时哈希，运行期只比较哈希（常数时间）。
- 匿名模式（`auth.mode: none`）用于本机调试，仍受匿名 RPM/配额约束。
- `X-Forwarded-For` 只在来源 IP ∈ `server.trusted_proxies` 时采信，否则用 RemoteAddr，防伪造。
- 每日配额按服务器本地时区自然日重置；重启清零。

---

## 9. 缓存

| 对象 | Key | TTL | 说明 |
|---|---|---|---|
| Search 响应 | 归一化 query+count+need_content+lang+sites… | 5min | 命中直接返回，不消耗 provider 配额 |
| Fetch 结果 | 归一化 URL | 30min | 成功才缓存 |
| 页面正文（need_content 内部） | 归一化 URL | 30min | 与 fetch 共用 |

实现为泛型 TTL-LRU（容量上限 + 惰性过期 + 定时清理），带 singleflight 合并。
缓存不区分 Key（同一 query 对所有用户一致），因此不含任何用户数据。

---

## 10. 可观测性

- 日志：`slog`，JSON 或 text；字段含 request_id、key_name、endpoint、status、duration_ms、providers、results。
- `GET /healthz`：`{"status":"ok","providers":[{"name","state","ok"}]}`，无鉴权。
- `GET /metrics`：Prometheus 文本（无鉴权，建议仅内网暴露）：
  `onesearch_requests_total{endpoint,status}`、`onesearch_request_duration_seconds{endpoint}`、
  `onesearch_provider_requests_total{provider,outcome}`、`onesearch_provider_breaker_state{provider}`、
  `onesearch_results_returned_total`、`onesearch_fetch_total{status}`、
  `onesearch_cache_hits_total{kind}`、`onesearch_rate_limited_total{reason}`、`onesearch_inflight`。

---

## 11. 配置项（完整示例见 `config.example.yaml`）

顶层：`server` / `auth` / `search` / `fetch` / `providers` / `cache` / `log`。
所有时长使用 `time.ParseDuration` 字符串（`5s`、`1m`）。
支持少量环境变量覆盖：`ONESEARCH_ADDR`、`ONESEARCH_AUTH_MODE`、`ONESEARCH_AUTH_KEYS`、
`ONESEARCH_SEARXNG_URL`、`ONESEARCH_LOG_LEVEL`、`ONESEARCH_LOG_FORMAT`。

默认配置只启用 `bingrss`（无需 Key 即可开箱使用）；`searxng`、`duckduckgo` 默认关闭。
`search.hedge_delay` 控制「已有足够结果后最多再等慢源多久」，默认 800ms。

启动时严格校验：未知 provider type、重复 Key 名、空 Key、非法时长、无可用 provider 均直接退出并打印原因。

---

## 12. 部署

- 单二进制：`go build -o onesearch ./cmd/onesearch && ./onesearch -config config.yaml`。
- Docker Compose：`onesearch` + `searxng`（内置最小 settings，开启 json 格式）。
- 无 SearXNG 也能跑：默认只启用 `bingrss`；compose 中通过 `ONESEARCH_SEARXNG_URL` 启用 SearXNG。

---

## 13. 验收标准

| 用例 | 期望 |
|---|---|
| 正常 search（假 provider） | 200；10 个字段齐全；`results` 非 null |
| 空结果 | 200 `{"results":[]}` |
| query 缺失/全空白/超 128 字符 | 400 |
| count 0 / 51 | 400（>50）；0 视为默认？→ 400（严格按 1～50） |
| 未知字段 | 200，忽略 |
| 无 Bearer / 错 Key | 401 |
| RPM 超限 | 429 + `Retry-After` |
| 每日配额耗尽 | 402 |
| Key 白名单不含 provider | 403 |
| need_content=true | 结果 `content` 非空（假页面服务器）；失败时 `""` |
| fetch 正常 | 200 `{title,content}`，content 为 Markdown |
| fetch 私网/loopback/`file://`/带 userinfo | 403 / 400 |
| fetch 无正文页 | 404 |
| fetch 超大页 | 413 |
| 缓存命中 | 第二次请求 provider 调用次数不增加 |
| 单 provider 超时 | 200 且其它源结果正常（部分降级） |
| 慢 provider 触发 hedge | 已有足够结果时在 `hedge_delay` 后返回，不等待慢源 |
| 熔断打开 | provider 请求快速失败，整体仍 200 |
| 并发闸门 | 超出并发返回 429 |
| 搜索结果字段语义 | 逐字段断言：空串 vs null、分数范围、日期格式 |

---

## 14. 已知限制

1. 分数与摘要为启发式，与官方不可比（§6）。
2. `after:`/`before:` 依赖结果自带日期，部分引擎无日期 → 过滤不完整。
3. 免费源不稳定：Bing RSS 有非商业用途限制，且对中文/混合语言长尾 query 的结果质量差、高频会被限流
   （实测同一 query 短时间内可能返回空 channel）；DDG 在中国大陆网络通常不可达。公开托管需自行评估合规。
4. Hedge 会牺牲少量召回：慢源在 `hedge_delay` 到期后被取消，其贡献不会进入融合结果。
5. 无 JS 渲染；SPA 页面抓取失败率高于官方。
6. 配额状态驻留内存，重启清零；多实例部署时各实例独立计数。
7. 无 `localization` 地理定位。
8. 不保证官方未来 schema 变更的自动跟进。

## 15. 后续（不在 v1）

- 可选 LLM 富化（summary 重写 / 语义重排），配置开关。
- Redis 共享缓存与配额。
- 管理 API：Key 动态增删、用量查询。
- 更多源：Wikipedia、GitHub、arXiv、Hacker News。
- 抓取重试与 JS 渲染兜底。
