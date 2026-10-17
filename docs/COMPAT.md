# 官方契约核对表（DeepSeek Search / Fetch API）

核对日期：2026-10-18
来源：

- <https://api-docs.deepseek.com/zh-cn/guides/search>（指南：参数、语法、错误、计费、限速）
- <https://api-docs.deepseek.com/zh-cn/api/search>（Search JSON Schema）
- <https://api-docs.deepseek.com/zh-cn/api/fetch>（Fetch JSON Schema）

图例：

- ✅ 已实现，且有自动化测试锁定
- 🧪 已用真实网络（Bing CN / Bing HK）端到端验证
- ⚠️ 有意偏差，已在 README / DESIGN 声明
- ❓ 按文档实现，但未与官方接口本身实测对照
- ➖ 自托管场景不适用

---

## 1. 端点与鉴权

| 官方条款 | 状态 | 实现 / 证据 |
|---|---|---|
| `POST /v1/tools/search` | ✅ | `internal/api/server.go` · `TestSearchHappyPath` |
| `POST /v1/tools/fetch` | ✅ | `TestFetchHappyPath` |
| `Authorization: Bearer <key>` | ✅ | `TestAuthRequired`、`TestRateLimit` |
| `Content-Type: application/json` | ✅ | 两个 handler 均 JSON 解码；`TestSearchValidation` 覆盖非法 JSON |
| 只读请求 | ✅ | 非 POST 返回 405（扩展码）；`TestMethodNotAllowed` |

## 2. Search 请求字段

| 条款 | 状态 | 实现 / 证据 |
|---|---|---|
| `query` 必填、1–128 Unicode 字符、不能全空白 | ✅ | `validateSearch`；`TestSearchValidation`、`TestParseValidation` |
| `count` 1–50，默认 10 | ✅ | 严格区间；`TestSearchValidation`（0/51 → 400），默认值 `TestSearchHappyPath` |
| `ori_question` ≤128，null 视为未提供 | ✅ | 长度校验 + 接收；`TestSearchValidation` |
| `localization` 接收、默认 `""` | ⚠️ | 接收但忽略（无 GeoIP）；README 已声明 |
| `need_content` 默认 false | ✅ | `TestNeedContentViaAPI`、`TestSearchBasic`（false 时 `content==""`） |
| 未知字段忽略 | ✅ | `json.Decoder` 默认行为；`TestSearchIgnoresUnknownFields` |
| 请求体超限 → 413 | ✅ | `MaxBytesReader` + `decodeBodyError`；`TestPayloadTooLarge` |
| 读请求体超时 → 408 | ✅ | `decodeBodyError`；`TestDecodeErrorMapping` |

## 3. 搜索语法

| 官方语法 | 状态 | 实现 / 证据 |
|---|---|---|
| `site:a OR site:b` | ✅ | 解析为 Sites，透传引擎 + 结果侧 `AllowsURL`；`TestParseOperators`、`TestAllowsURL` |
| `-site:x` | ✅ | `TestParseOperators` |
| `"精确短语"` / `-"短语"` | ✅ | `TestParseOperators` |
| `-词` | ✅ | `TestParseOperators`、`TestExcludesText` |
| `lang:zh,en` | ✅ | 转 `language`/`setlang`/`search_lang`；`TestSearXNG` 断言 `language=zh` |
| `after:` / `before:` | ⚠️ | 解析为时间界，**结果后置过滤**（日期未知保留）；`TestAllowsDate`；README §差异 |
| 语法原样透传 | ✅ | `EngineString()` 重建后交给引擎；`TestParseOperators` 断言重建结果 |

## 4. Search 响应

| 条款 | 状态 | 实现 / 证据 |
|---|---|---|
| `{"results":[...]}`，无结果为 `[]` | ✅ | `NewSearchResponse` 保证非 null；`TestSearchEmptyResults` |
| 10 个字段始终存在 | ✅ | `TestSearchHappyPath` 逐字段断言（字段数 == 10） |
| 可空字段输出 `null`，字符串缺失为 `""` | ✅ | `TestSearchResultNullables`、`TestSearchHappyPath`（date null / content ""） |
| `rerank_score` 0–3 | ✅ | `rank.score` clamp + round；`TestFuseScoreRangeAndOrdering`、`TestSearchHappyPath` |
| `authority` -1–4 | ✅ | `siteinfo.Authority` 默认 -1；`TestAuthority` |
| `date` UTC RFC3339，未知 null | ✅ | `enrich.DatePtr`；`TestSearchHappyPath` 用 `time.RFC3339` 反解 |
| `site_name` / `site_icon` 可空 | ✅ | `enrich.SiteName` / `Icon`；`TestSearchHappyPath`、`TestSearchResultNullables` |
| `snippet` 适合前端（约 100–200 字） | ✅ | `enrich.Snippet` 截断；`TestSnippetCleansAndClips` |
| `summary` 适合模型（约 600–800 字） | ⚠️ | 有正文时优先取正文；正文短时按实际长度输出，不硬凑；`TestSummaryFromContent` |
| `content` 仅 `need_content=true` 时填充 | ✅ | `TestNeedContentViaAPI`、`TestSearchBasic` |

## 5. Fetch

| 条款 | 状态 | 实现 / 证据 |
|---|---|---|
| `url` 必填、≤8192、http/https、含 host、无 userinfo | ✅ | `fetchguard.ValidateURL`；`TestValidateURL`、`TestFetchValidation` |
| 返回 `{title, content}`，content 为 Markdown | ✅ | `extract` + `fetchsvc`；`TestFetchHappyPath`、`TestFetchExtractsMarkdown`；🧪 实测 api-docs.deepseek.com（5889 字 Markdown） |
| title 缺失为空字符串 | ✅ | `extract.Page.Title` 可空；`TestExtractPlainText`、`TestFetchHappyPath` |
| 无法提取正文 → 404 | ✅ | `TestFetchNoContent`、`TestFetchUnsupportedContentType` |
| 目标站点报错 → 404 | ✅ | `TestFetchUpstreamErrorBecomesNotFound` |
| 安全策略拒绝 → 403 | ✅ | SSRF 防护；`TestFetchSSRFBlocked`、`TestFetchBlocksSSRF` |
| 页面超限 → 413 | ✅ | `TestFetchTooLarge` |
| 下游超时 → 504 | ✅ | `TestFetchTimeout` |

## 6. 错误信封与状态码

| 条款 | 状态 | 实现 / 证据 |
|---|---|---|
| `{error:{message,type,param:null,code}}` | ✅ | `httpx.WriteError`；`TestWriteErrorEnvelope`（含 `"param":null`） |
| 400 参数非法 | ✅ | `TestSearchValidation`、`TestFetchValidation` |
| 401 Key 缺失/无效 | ✅ | `TestAuthRequired` |
| 402 配额/余额不足 | ⚠️ | 本服务用于**每日配额耗尽**（非账户余额）；`TestDailyQuota` |
| 403 策略拒绝 | ✅ | SSRF 拦截：`TestFetchSSRFBlocked`、`TestFetchBlocksSSRF`；provider 白名单是**过滤**而非 403（`TestProviderAllowlistFilters`） |
| 404 无正文 | ✅ | 见 §5 |
| 408 读超时 | ✅ | `decodeBodyError`；`TestDecodeErrorMapping` |
| 413 请求体/页面超限 | ✅ | `TestPayloadTooLarge`、`TestFetchTooLarge` |
| 429 限流 + `Retry-After` | ✅ | `TestRateLimit`（断言 Retry-After） |
| 500 / 504 | ✅ | `AsAPIError` 兜底 + `UpstreamTimeout`；`TestErrorConstructors` |
| 405 方法不对 | ⚠️ | 本服务扩展（官方未文档化）；`TestMethodNotAllowed` |

## 7. 计费与限速

| 官方条款 | 状态 | 说明 |
|---|---|---|
| ¥5/1000 次，仅成功请求计费 | ➖ | 自托管不收费，无计费实现 |
| Search / Fetch 各 5 QPS（账号级） | ⚠️ | 以「每 Key RPM + 令牌桶 + 全局并发闸门 + provider RPM」替代；语义不同 |
| Fetch 另有目标域名级 QPS | ❌ | 未实现 per-host 限速（仅有全局并发）；见待办 |
| 408/429/5xx 等待重试 | ➖ | 客户端侧建议；服务端已在 429 返回 Retry-After |

## 8. 是否与官方接口实测对过

**没有。** 本机环境未配置可用的 DeepSeek API Key（`DEEPSEEK_API_KEY` 等均不存在），
也没有使用 `~/.dsh/.credentials.yaml` 中的凭据。因此：

- 上述 ✅ 均来自「按官方文档实现 + 自动化契约测试」；
- 🧪 仅覆盖真实搜索源（Bing CN / Bing HK）与真实网页抓取的端到端；
- 以下内容属于「按文档实现、未实测」：官方 `count` 越界的实际文案、官方 402 的触发条件、
  官方 403 的具体场景、官方对未知字段/超长 URL 的实际容忍度、官方 summary 的实际长度分布。

## 9. 待办（把 ❓ 变成实测）

1. **官方 golden 对照**：设置 `DEEPSEEK_API_KEY` 后跑一组同输入对照（状态码、字段集合、
   可空语义、边界文案），差异写进本表。
2. **Fetch 目标域限速**：`fetch` 增加 per-host 令牌桶，超限返回 429。
3. 若官方后续变更 schema：以本表为回归清单，逐条更新。
