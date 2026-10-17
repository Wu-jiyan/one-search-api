# one-search-api

自托管、与 **DeepSeek Search / Fetch API 格式兼容**的搜索与抓取服务。
用 Go 写成单二进制，后端聚合免费搜索源（SearXNG / Bing RSS / DuckDuckGo / Brave / Tavily），
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
| 多源聚合 | SearXNG、Bing RSS、DuckDuckGo HTML、Brave、Tavily，可单启用/加权/限速 |
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
      "rerank_score": 2.77,
      "title": "deepseek-ai/DeepSeek-V4-Pro-0813 · Hugging Face",
      "url": "https://huggingface.co/deepseek-ai/DeepSeek-V4-Pro-0813",
      "snippet": "...",
      "summary": "...",
      "content": "",
      "site_name": "huggingface.co",
      "site_icon": "https://huggingface.co/favicon.ico",
      "date": "2026-10-16T14:38:00Z",
      "authority": -1
    }
  ]
}
```

> 注意：免费的 Bing RSS 对中文/混合语言长尾 query 的结果质量一般，且高频请求会被限流。
> 想要稳定质量，请用下面的 SearXNG 部署。

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
| `searxng` | 否（可选 token） | **推荐主源**；需在 SearXNG `settings.yml` 的 `search.formats` 中加 `json` |
| `bingrss` | 否 | 无需 Key；微软声明仅限个人非商业用途；结果质量不稳定、易被限流 |
| `duckduckgo` | 否 | 解析 HTML；可能触发人机验证；**中国大陆网络通常不可达** |
| `brave` | 是 | 免费 2000 次/月，质量好 |
| `tavily` | 是 | 免费 1000 次/月，自带摘要能力 |

---

## API

### `POST /v1/tools/search`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `query` | string | 是 | 1～128 字符；支持 `site:`、`-site:`、`"短语"`、`-词`、`lang:`、`after:`、`before:` |
| `count` | int | 否 | 1～50，默认 10 |
| `ori_question` | string/null | 否 | ≤128 字符；v1 仅记录，不参与排序 |
| `localization` | string | 否 | 接收但忽略（无 GeoIP） |
| `need_content` | bool | 否 | 为 true 时对前 N 条抓取正文填入 `content` |

响应固定 10 个字段，`results` 恒为数组（无结果 `[]`）。可空字段输出 `null`。

### `POST /v1/tools/fetch`

```json
{"url": "https://example.com/article"}
```

返回 `{"title": "...", "content": "# Markdown"}`；抽取不到正文返回 404。

### 错误码

| HTTP | type | 触发 |
|---|---|---|
| 400 | invalid_request_error | JSON/字段非法、query 全空白、count 越界、URL 非法 |
| 401 | authentication_error | Bearer 缺失/错误 |
| 402 | insufficient_quota | 每日配额耗尽 |
| 403 | permission_error | SSRF 拦截 |
| 404 | not_found_error | 页面无正文 / 上游不可用 |
| 413 | invalid_request_error | 请求体或页面超限 |
| 429 | rate_limit_error | RPM 超限 / 全局并发满 |
| 500 / 504 | server_error / timeout_error | 内部错误 / 上游超时 |

官方只文档化了 `invalid_request_error`；其余 code 是本服务的扩展，信封形状保持一致。
429/402 带 `Retry-After`，所有响应带 `X-Request-Id`。

---

## 与官方实现的差异（务必先读）

1. **分数和摘要不可比。** `rerank_score` 是 RRF + 权威性 + 时效性的启发式映射；
   `authority` 是域名分级表；`summary` 是正文截断或摘要拼接。字段形状与取值范围一致，
   但如果你按官方语义做阈值（如 `score>=1.5`、`authority>=3`），结果分布会不同。
2. **`after:` / `before:` 是后置过滤。** 只在结果自带日期时生效，日期未知的结果会保留。
3. **`localization` 不生效**（没有 GeoIP 库）。
4. **`ori_question` 不参与排序**（v1 无 LLM 富化）。
5. **provider 白名单是过滤，不是报错**：过滤后没有可用源时返回 `{"results":[]}`。
6. **`need_content` 有上限**：默认只对前 10 条抓正文，失败的结果 `content` 为 `""`。
7. **不支持 JS 渲染**：SPA 页面抓取失败率高于官方。

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
onesearch_provider_requests_total{provider,outcome}
onesearch_provider_breaker_state{provider}   # 由 /healthz 暴露
onesearch_fetch_total{status}
onesearch_cache_hits_total{kind}
onesearch_rate_limited_total{reason}
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

- **Bing RSS** 的版权声明写明「仅限个人非商业用途在 RSS 聚合器中呈现」。自用没问题，公开托管需自行评估。
- **DuckDuckGo** 解析 HTML 可能违反其 ToS；访问量大了会被验证码拦截。
- **SearXNG** 本身是 AGPL 软件，自托管没问题，但它也可能被上游引擎封 IP。

---

## 开发

```bash
go vet ./...
go test ./...
go test -race ./...      # 建议
go build -o onesearch ./cmd/onesearch
```

项目结构：

```
cmd/onesearch/            入口
internal/api/             路由、中间件、两个 handler、/healthz、/metrics
internal/auth/            Key 鉴权、RPM、每日配额
internal/cache/           TTL-LRU + singleflight
internal/config/          YAML + 环境变量 + 校验
internal/enrich/          snippet/summary/site/date 富化
internal/extract/         HTML → 标题 / 正文 / Markdown
internal/fetchguard/      SSRF 校验与安全 Dialer
internal/fetchsvc/        抓取编排与缓存
internal/httpx/           线协议类型与错误信封
internal/limit/           令牌桶、信号量、熔断器
internal/obs/             日志与指标
internal/provider/        5 个搜索源实现
internal/query/           搜索语法解析
internal/rank/            归一化、去重、RRF 融合、打分
internal/search/          并发扇出、hedge、正文补齐
internal/siteinfo/        域名 → authority / 站点名 / favicon
docs/DESIGN.md            设计文档（契约、字段映射、验收标准）
```

---

## 已知限制

- 免费源不稳定：Bing RSS 质量波动且会被限流；DDG 在部分网络不可达；SearXNG 依赖自建实例。
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
