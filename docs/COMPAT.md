# 官方契约核对表（DeepSeek Search / Fetch API）

核对日期：2026-10-18（含**官方接口实测**，见 §8）

- 文档来源：<https://api-docs.deepseek.com/zh-cn/guides/search>、`/zh-cn/api/search`、`/zh-cn/api/fetch`
- 实测方式：用真实 API Key 向 `https://api.deepseek.com` 发同输入请求，逐项记录状态码、
  错误 `type`/`code`/`message`、响应字段与长度分布；结论已固化为可重跑测试
  `internal/compat/official_test.go`（见 §10）。

图例：

- ✅ 已实现，且有自动化测试锁定
- 🧪 已与官方接口**逐项实测对照**
- ⚠️ 有意偏差，已在 README / DESIGN 声明
- ➖ 自托管场景不适用

---

## 1. 端点与鉴权

| 条款 | 状态 | 实测 / 实现 |
|---|---|---|
| `POST /v1/tools/search` | 🧪 | 200，`Content-Type: application/json`（无 charset） |
| `POST /v1/tools/fetch` | 🧪 | 同上 |
| `Authorization: Bearer <key>` | 🧪 | 缺失 → 401 `Authentication Fails (missing key)`；错误 Key → 401 `Authentication Fails, Your api key: ****9999 is invalid` |
| **鉴权先于一切校验** | 🧪 | 无 Key 时即使 body 非法 / `Content-Type` 错误也返回 401 |
| `Content-Type: application/json` 必填 | 🧪 | 缺失或非 JSON → 400 `Content-Type must be application/json` |
| 请求体必须是 JSON 对象 | 🧪 | 空 body / 数组 / `null` → 400 `Request body must be a JSON object` |
| 非 POST 方法 | 🧪 | 405，**空响应体**，响应头 `Allow: POST` |
| CORS | 🧪 | 回显 Origin、`allow-credentials: true`、`OPTIONS` 预检 200（`allow-methods: POST`） |

## 2. Search 请求字段

| 条款 | 状态 | 实测 / 实现 |
|---|---|---|
| `query` 必填 | 🧪 | `""` / 全空白 → 400 `'query' must not be empty`；字段缺失 → 400（serde 文案 `missing field query`） |
| `query` ≤128 **字符（rune）** | 🧪 | 128 个 ASCII / 汉字 / emoji 均通过，129 个 → 400 `'query' must be at most 128 characters`（按码点而非字节） |
| `count` 1–50，默认 10 | 🧪 | 省略 → 10 条；`0`/`51` → 400 `'count' must be between 1 and 50`；`50` → 50 条 |
| `count` 类型严格 | 🧪 | `1.5`、`"5"`、`null` → 400（`invalid type: …, expected u32`）⚠️ 我们对 `null` 宽松处理，视为未提供 |
| `ori_question` 1–128 | 🧪 | 空串与 129 字符均 → 400 `'ori_question' must be between 1 and 128 characters` |
| `localization` 接收 | 🧪 | 传入任意值均 200，对结果无可观察影响（无 GeoIP）⚠️ 我们忽略该字段 |
| `need_content` 默认 false | 🧪 | `false` 时 `content` 恒为 `""`；`true` 时**全部结果**都填正文 |
| `need_content` 类型严格 | 🧪 | `"true"` → 400（`expected a boolean`） |
| 未知字段忽略 | 🧪 | 加 `totally_unknown: 42` 与不加的响应字节数完全相同 |
| 请求体超限 → 413 | 🧪 | 2 MB body → 413，`type=payload_too_large_error`、`code=invalid_request_error` |
| 非法 JSON → 400 | 🧪 | `type=code=invalid_request_error`，消息以 `Invalid JSON request: …` 开头 |

## 3. 搜索语法

| 语法 | 状态 | 实测 / 实现 |
|---|---|---|
| `site:a OR site:b` | 🧪 | 官方返回限定域结果；我们解析为 Sites 并做结果侧过滤 |
| `-site:x` | ✅ | `TestParseOperators` |
| `"精确短语"` / `-"短语"` | ✅ | `TestParseOperators` |
| `-词` | 🧪 | 官方 `python -django` 生效；我们结果侧排除，`TestExcludesText` |
| `lang:zh,en` | 🧪 | 官方按语言给结果；我们转成引擎 language/setlang 参数 |
| `after:` / `before:` | 🧪 | 官方按发布时间过滤；我们**结果后置过滤**（日期未知保留）⚠️ |
| 语法原样透传 | ✅ | `EngineString()` 重建后交引擎，`TestParseOperators` |

## 4. Search 响应

字段顺序实测为：`title, site_name, site_icon, authority, rerank_score, date, url, summary, snippet, content`
（本实现已按此顺序序列化）。

| 条款 | 状态 | 实测数据 / 实现 |
|---|---|---|
| `{"results":[...]}` | 🧪 | 命中时数组；无匹配时 `{"results":[]}` |
| 10 个字段始终存在 | 🧪 | 逐字段核对（字段数 == 10） |
| 可空字段为 `null` | 🧪 | `date` 可为 `null`（50 条里 2 条）；`site_name`/`site_icon` 实测未出现 null，我们仍允许 null |
| `date` 格式 | 🧪 | `2026-04-26T16:00:00+00:00`（**显式 +00:00，不是 Z**）；未知为 `null` |
| `rerank_score` | 🧪 | 原始浮点全精度（如 `2.289104461669922`）；实测区间 0.10–2.29（乱码查询 0.10–0.19）⚠️ 我们为启发式分数且保留两位小数，不可与官方逐值比较 |
| `authority` | 🧪 | 实测 1–4（乱码查询出现 1/2/3）⚠️ 我们为域名分级表，范围 -1–4 |
| `title` 可为空串 | 🧪 | need_content 场景 5 条里 2 条 `"title":""` |
| `site_name` | 🧪 | 中文站返回中文名（`央广网`、`证券时报`）；我们为内置表 + 域名兜底 |
| `site_icon` | 🧪 | 恒为 `https://cdn.deepseek.com/site-icons/<可注册域>` ⚠️ 我们输出自有模板（默认 `<host>/favicon.ico`），可用 `site_icon_template` 改成官方 CDN |
| `snippet` 长度 | 🧪 | 80–364 字符，均值 126–178（随查询变化）；本实现 clip 到 360 内 |
| `summary` 长度 | 🧪 | 无 need_content：144–2504（均值 1130–1364）；有 need_content：2654–4386（均值 3328）⚠️ 本实现合成摘要，content 可得时目标 800–1600，仅 snippet 时 ≤1200 |
| `content` 填充策略 | 🧪 | 官方**抓全部结果**（8/8、15/15、30/30 均填满）⚠️ 本实现受 `need_content_max_results` 限制（默认 20），失败项为 `""` |
| 乱码查询不给空 | 🧪 | 乱码串仍返回 10 条低分结果 ⚠️ 我们可能因离题过滤返回更少或空 |

## 5. Fetch

| 条款 | 状态 | 实测 / 实现 |
|---|---|---|
| 返回 `{content, title}`（Markdown） | 🧪 | `example.com` → `content` 931 字符、`title="Example Domain"`；`api-docs.deepseek.com` 页 6402 字符 |
| 非 HTML 文档 | 🧪 | **接受** `text/plain`（RFC 正文 20540 字符）、`text/css`、`text/javascript`、`text/markdown`、`application/json`、无 `Content-Type`；**拒绝**（404）`text/xml`、`application/xml`、`application/xhtml+xml`、`text/csv`、PDF、图片、`octet-stream` |
| 无 title 的文档 | 🧪 | 纯文本 / JSON / CSS / JS 返回 `"title":""` |
| `url` 校验文案 | 🧪 | 空/空白 → `'url' must be a valid HTTP or HTTPS URL`；`file:`/`ftp:` → `'url' must be an HTTP or HTTPS URL with a host`；带账号 → `'url' must not contain credentials`；>8192 → `'url' must be at most 8192 characters` |
| 上游 4xx/5xx → 404 | 🧪 | `type=not_found_error`、`code=invalid_request_error`、消息固定 `failed to fetch URL` |
| 私网/loopback/metadata → 404 | 🧪 | 官方不暴露 SSRF 策略，统一 `failed to fetch URL`（本实现已对齐） |
| 超时 → 504 | 🧪 | `type=code=gateway_timeout_error`、消息 `Request timeout` |
| 重定向 | 🧪 | 连续 12 次跳转仍 200（本实现 `max_redirects` 默认 15） |
| 页面超限 → 413 | ✅ | 官方未实测；本实现 `type=payload_too_large_error` |
| 抓取栈能力 | 🧪 | 官方能取到对 curl 返回 403 的站点（`www.gnu.org`）⚠️ 我们无 JS 渲染、无住宅代理 |

## 6. 错误信封与状态码（逐项实测）

信封恒为 `{"error":{"message","type","param":null,"code"}}`，且**每条 message 末尾带
`(request_id: <uuid>)`**（本实现也追加该后缀）。

| HTTP | `type` | `code` | 触发场景（实测） |
|---|---|---|---|
| 400 | `invalid_request_error` | `invalid_request_error` | 参数非法、JSON 非法、`Content-Type` 不对、body 非对象 |
| 401 | `authentication_error` | `invalid_request_error` | Key 缺失 / 无效 |
| 402 | — | — | 文档写「余额/配额不足」，本次未触发 ➖ 本实现用于每日配额耗尽 |
| 403 | — | — | 本次未观测到（SSRF 用的是 404）➖ 本实现已不再返回 403 |
| 404 | `not_found_error` | `invalid_request_error` | fetch 一切失败：上游错误、私网、抓不到正文、媒体类型不支持 |
| 405 | —（**空体**） | — | 方法不对，响应头 `Allow: POST` |
| 408 | — | — | 本次未观测到（读 body 超时）→ 本实现沿用 |
| 413 | `payload_too_large_error` | `invalid_request_error` | 请求体超限 |
| 429 | — | — | 未能在探测中触发（文档称账号级 5 QPS）→ 本实现为 RPM/并发限流 |
| 504 | `gateway_timeout_error` | `gateway_timeout_error` | fetch 目标超时 |

## 7. 计费与限速

| 条款 | 状态 | 说明 |
|---|---|---|
| ¥5 / 1000 次，仅成功计费 | ➖ | 自托管不收费 |
| Search / Fetch 各 5 QPS（账号级） | ⚠️ | 以「每 Key RPM + 令牌桶 + 全局并发闸门 + provider RPM」替代 |
| Fetch 目标域名级 QPS | ❌ | 未实现 per-host 限速（仅全局并发）；见 §10 待办 |

## 8. 与官方接口实测对照（2026-10-18）

用真实 Key 对 `api.deepseek.com` 跑了 60+ 次请求，覆盖：正常搜索、`count` 边界、
`need_content`、乱码查询、全部 4xx/405/413/504 分支、fetch 的 18 种目标（含 SSRF、超时、
各媒体类型、12 级跳转、非 HTTP scheme）。

**结论：请求/响应信封、字段集合、可空语义、错误 `type`/`code`、状态码分支均已对齐。**
本轮实测后修正的实现项：

1. `date` 改为 `+00:00` 偏移（原为 `Z`）。
2. 401 的 `code` 改为 `invalid_request_error`（原为 `invalid_api_key`）。
3. **鉴权提前到 body 解析之前**（原为解析 → 校验 → 鉴权）。
4. 新增 `Content-Type: application/json` 强制校验、空体/非对象体拒绝。
5. 405 改为空响应体 + `Allow: POST`（原返回 JSON 信封）。
6. 413 `type` → `payload_too_large_error`；504 `type/code` → `gateway_timeout_error`。
7. 404 的 `code` → `invalid_request_error`。
8. fetch 的 SSRF 拦截由 403 改为官方口径的 404 `failed to fetch URL`。
9. fetch 媒体白名单按实测放宽（纯文本/JSON/CSS/JS 可抓），XML/CSV/PDF/图片仍 404。
10. fetch 的 URL 校验文案逐字对齐官方。
11. 新增 CORS（回显 Origin / 预检 200 / 暴露请求 ID）。
12. 错误消息追加 `(request_id: …)` 后缀。
13. 搜索响应、fetch 响应的字段顺序按官方线序重排。
14. `snippet`/`summary` 长度上限按实测分布上调；`max_redirects` 5 → 15。
15. 401 文案对齐（`Authentication Fails (missing key)` / `****xxxx is invalid`）。

## 9. 仍保留的偏差（有意）

| # | 偏差 | 原因 |
|---|---|---|
| 1 | `rerank_score` / `authority` / `summary` 为启发式 | 无自研搜索引擎与 LLM 摘要；仅保证字段形状与取值范围 |
| 2 | `after:` / `before:` 为结果后置过滤 | 无索引级时间过滤 |
| 3 | `localization` 不生效 | 无 GeoIP |
| 4 | `ori_question` 不参与排序 | v1 无 LLM 富化 |
| 5 | `need_content` 只抓前 N 条（默认 20） | 抓满 50 页在自托管下过慢；官方抓全部 |
| 6 | `count: null` 等类型边界更宽松 | 正常客户端不受影响 |
| 7 | 乱码查询可能返回空数组 | 有离题结果过滤；官方返回 10 条低分结果 |
| 8 | `site_icon` 用自有模板 | 不依赖官方 CDN；可配置成 `https://cdn.deepseek.com/site-icons/{host}` |
| 9 | 402 用于每日配额 | 自托管无账户余额概念 |
| 10 | 429 语义为 RPM/并发 | 官方为账号级 QPS |
| 11 | 无 fetch per-host 限速 | 见 §10 待办 |
| 12 | 无 JS 渲染 | SPA 抓取率低于官方 |

## 10. 如何复跑这份对照

```bash
# 官方 golden 测试（需要 Key，会消耗少量额度）
DEEPSEEK_API_KEY=sk-... go test ./internal/compat -tags official -v -count=1

# 本实现自身的契约测试（离线）
go test ./...
```

`internal/compat/official_test.go` 里的每个断言都来自 §8 的实测；官方若变更 schema，
这里会第一时间失败。待办：

1. 观察官方 402 / 429 / 408 的真实文案（本次未触发），补进 §6。
2. fetch 增加 per-host 令牌桶（官方文档提到域名级 QPS）。
3. 若官方 schema 变更：以本表为回归清单逐条更新。
