# one-search-api

> Self-hosted, DeepSeek Search / Fetch API-compatible web search & fetch service in Go.
> One `base_url` for your agent, many free search backends behind it.

[![CI](https://github.com/Wu-jiyan/one-search-api/actions/workflows/ci.yml/badge.svg)](https://github.com/Wu-jiyan/one-search-api/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Wu-jiyan/one-search-api.svg)](https://github.com/Wu-jiyan/one-search-api/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/Wu-jiyan/one-search-api.svg)](https://pkg.go.dev/github.com/Wu-jiyan/one-search-api)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27-00ADD8.svg)](go.mod)
[![Contract](https://img.shields.io/badge/contract-measured%20vs%20official-brightgreen.svg)](docs/COMPAT.md)

**English** | [中文](README.md)

A single Go binary that aggregates free search backends (SearXNG / Bing CN & intl /
DuckDuckGo / Brave / Tavily) and exposes exactly the two endpoints DeepSeek's agent tools use:

```
POST /v1/tools/search   → {"results":[{title,site_name,site_icon,authority,rerank_score,date,url,summary,snippet,content}]}
POST /v1/tools/fetch    → {"content":"# page as Markdown","title":"..."}
```

Point your agent / RAG pipeline / client at this service instead of
`https://api.deepseek.com`, and keep the same tool schema. Built for self-use plus a few
friends: no billing, but real API keys, quotas, concurrency limits and abuse protection.

---

## Why

- DeepSeek bills Search / Fetch at ¥5 per 1000 successful requests, and server-side calls
  from DSH / Claude Code / Codex become billable on **2026-11-01 00:00 (UTC+8)**.
- The official endpoints are limited to 5 QPS per account and require a DeepSeek key.
- Self-hosting gives you: no per-request billing, any backend you like, one tool schema for
  every model/client, and room for your own data sources.

To be fair: ¥5/1000 is cheap — a few hundred searches a day is tens of yuan a month. The
real value here is **independence and control**, not saving money.

---

## Features

| Area | What you get |
|---|---|
| Wire compatibility | Request/response shapes, nullable-field semantics, OpenAI-style error envelope |
| Multi-source | SearXNG, Bing (CN/intl × RSS/HTML), DuckDuckGo, Brave, Tavily; per-provider enable/weight/rate limit |
| Retrieval | URL normalization → dedupe → RRF fusion → domain diversity → heuristic scoring → field enrichment |
| Content | readability + HTML→Markdown, charset sniffing, caching, concurrent `need_content` backfill |
| Auth & quota | Per key: RPM token bucket, daily quota, provider allowlist, max results |
| Anti-abuse | Body size cap, field validation, global concurrency gate, provider circuit breaker, per-provider rate limit |
| SSRF protection | Private/loopback/link-local/CGNAT blocking, pinned IP dialer (DNS rebinding), per-hop redirect checks, port allowlist |
| Observability | Structured `slog` logs, `/healthz`, Prometheus `/metrics` |
| Deployment | Single binary; Dockerfile + docker-compose (with SearXNG) |

---

## Quick start (no SearXNG needed)

### Prebuilt binaries / Docker

- **Binaries**: grab `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` or
  `windows/amd64` from [Releases](https://github.com/Wu-jiyan/one-search-api/releases);
  the archive includes `config.example.yaml`.
- **Docker**: `docker build -t one-search-api . && docker run -p 8080:8080 one-search-api`
  (see the compose setup below for SearXNG).
- **go install**: `go install github.com/Wu-jiyan/one-search-api/cmd/onesearch@latest`

The default config enables **Bing RSS** only, which works out of the box:

```bash
go build -o onesearch ./cmd/onesearch
./onesearch          # listens on :8080, auth.mode=none (local debugging only)
```

```bash
curl -s http://127.0.0.1:8080/v1/tools/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"DeepSeek V4 release","count":3}'
```

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

> Field order matches the reference API:
> `title, site_name, site_icon, authority, rerank_score, date, url, summary, snippet, content`.

> **About Bing channels**: RSS and HTML are two renderings of the same result set — there is
> no "RSS is lower quality". The real failure modes are `200 + empty channel` (soft rate
> limiting) and replaying another query's cached page. Both are handled (same-channel retry,
> HTML fallback, batch-relevance filtering); see
> [Bing channels & fingerprints](#bing-channels--fingerprints-cn--intl).

---

## Recommended deployment (Docker Compose + SearXNG)

```bash
docker compose up -d --build
```

- `searxng` — ships `deploy/searxng/settings.yml` with `json` output enabled, binds `127.0.0.1:8888`.
- `onesearch` — binds `127.0.0.1:8080`, reaches SearXNG via `ONESEARCH_SEARXNG_URL=http://searxng:8080`.

```bash
curl -s http://127.0.0.1:8080/v1/tools/search \
  -H 'Authorization: Bearer sk-change-me-please' \
  -H 'Content-Type: application/json' \
  -d '{"query":"golang context cancellation","count":5}'
```

The default key is `sk-change-me-please` — **change it before exposing the service**.

### DeepSeek Harness (DSH)

DSH ships a `@deepseek-ai/dsh-web-deepseek-api` provider. Set its `baseURL` to
`http://127.0.0.1:8080/v1/tools` and DSH's `web_search` / `web_fetch` run on this service
(verified live, hot-reloaded without restart). Full steps, verification and rollback:
**[docs/DSH.md](docs/DSH.md)** (Chinese).

---

## Configuration

See [`config.example.yaml`](config.example.yaml). Precedence: **defaults < YAML file < env**.

```bash
./onesearch -config config.yaml
```

### Minimal config (multiple keys + quotas)

```yaml
auth:
  mode: key
  keys:
    - name: me
      token: "sk-onesearch-change-me"
      rpm: 120          # requests per minute
      burst: 20         # token bucket size
      daily_quota: 5000 # requests per day, 0 = unlimited
      max_results: 50
      allowed_providers: []   # empty = all
    - name: friend
      token: "sk-onesearch-friend"
      rpm: 60
      daily_quota: 1500
      max_results: 20
      allowed_providers: [searxng, bing]
```

`token` accepts plaintext or `sha256:<hex>` (recommended: `echo -n 'sk-xxx' | sha256sum`).
Plaintext is hashed at startup; comparisons are constant-time.

### Environment overrides

| Variable | Purpose |
|---|---|
| `ONESEARCH_ADDR` | Listen address, e.g. `:8080` |
| `ONESEARCH_AUTH_MODE` | `none` / `key` |
| `ONESEARCH_AUTH_KEYS` | `name:token[:rpm[:daily_quota[:max_results]]]`, comma separated |
| `ONESEARCH_SEARXNG_URL` | SearXNG URL; enables that provider |
| `ONESEARCH_LOG_LEVEL` / `ONESEARCH_LOG_FORMAT` | Log level / `json` or `text` |

### Providers

| type | Key needed | Notes |
|---|---|---|
| `searxng` | no (optional token) | **Recommended primary**; requires `json` in SearXNG `search.formats`; paginates via `pageno`, so `count>20` can be filled together with Brave |
| `bingrss` | no | Bing CN/intl RSS; ~5 KB, best `date` coverage, **default channel**; retries empty channels, falls back to HTML; fixed 10 per page, tries `&first=` paging when `count>10` (stops when a page adds nothing) |
| `binghtml` | no | Bing CN/intl HTML; ~100 KB, richer snippets; same adaptive `&first=` paging |
| `duckduckgo` | no | HTML parsing; may hit a CAPTCHA; usually unreachable from mainland China |
| `brave` | yes | Free 2,000/month, good quality |
| `tavily` | yes | Free 1,000/month, built-in summaries |

### Bing channels & fingerprints (CN / intl)

Bing's RSS and HTML share one backend and differ only in rendering:

| Channel | Size | Notes |
|---|---|---|
| `bingrss` | ~5 KB | carries `pubDate`, best `date` coverage; default |
| `binghtml` | ~100 KB | `.b_caption` snippets, `.news_dt` dates, richer structure |

"Four channels" = 2 renderings × 2 markets, expressed as four provider instances:

```yaml
- {name: bing,           type: bingrss,  base_url: https://cn.bing.com,  market: zh-CN}
- {name: bing-html,      type: binghtml, base_url: https://cn.bing.com,  market: zh-CN}
- {name: bing-intl,      type: bingrss,  base_url: https://www.bing.com, market: en-WW}
- {name: bing-intl-html, type: binghtml, base_url: https://www.bing.com, market: en-WW}
```

> Outside mainland China `cn.bing.com` answers with a **301 to `www.bing.com`** (rewriting
> the path), so deploy intl with `www.bing.com`; use `cn.bing.com` inside China.

**Automatic anti-bot behavior (on by default, nothing to configure)**

- Per-provider session with **background warm-up at startup** (re-warms after invalidation),
  collecting real cookies (`MUID / _EDGE_S / SRCHD / SRCHUSR / SRCHUID / _SS`, …).
- Every session **generates** a browser fingerprint: UA (random Windows/macOS/Linux + Chrome
  major), `sec-ch-ua`, platform, `Accept-Language`, a 32-hex uppercase `cvid`.
- Search URLs carry the parameters a real browser sends
  (`form=QBLH&sp=-1&lq=0&pq=<last query>&sc=<generated>&qs=n&sk=&cvid=…&ghsh=0&ghacc=0&ghpl=`),
  plus `setmkt=`, `Referer` and `Sec-Fetch-*`.
- `mobile: true` switches to a mobile fingerprint (`PC=EMMX01&form=LBT003&scope=web&darkschemeovr=1`).
  Measured: mobile HTML returns only 5 results per page, so desktop is the default; RSS is unaffected.
- Empty results, errors or off-topic batches **rotate fingerprint + cookie jar** and retry on the
  same channel; RSS still failing falls back to HTML parsing.
- Whole off-topic batches (Bing replaying another query's cache) are dropped by
  **batch-relevance filtering** so they never pollute ranking.

Manual overrides: `user_agent` / `referer` / `cookies` / `mobile` / `warmup: false` / `extra_params`.

---

## API

### `POST /v1/tools/search`

| Field | Type | Required | Notes |
|---|---|---|---|
| `query` | string | yes | 1–128 characters; supports `site:`, `-site:`, `"phrase"`, `-word`, `lang:`, `after:`, `before:` |
| `count` | int | no | 1–50, default 10 |
| `ori_question` | string/null | no | ≤128 chars; recorded in v1, not used for ranking |
| `localization` | string | no | accepted, ignored (no GeoIP) |
| `need_content` | bool | no | when true, fetch body text into `content` for every result (cap 50, same as the reference) |

`Content-Type: application/json` is required and the body must be a JSON object; authentication
runs before validation (same as the reference: a bad key yields 401 even for an invalid body).

The response always has 10 fields in the reference order; `results` is always an array
(`[]` when empty). Nullable fields render as `null`; `date` is UTC with a `+00:00` offset.

### `POST /v1/tools/fetch`

```json
{"url": "https://example.com/article"}
```

Returns `{"content": "# Markdown", "title": "..."}`. Matching the reference behavior,
`text/plain`, `text/markdown`, `text/css`, `text/javascript` and `application/json` are returned
verbatim; XML / CSV / PDF / images are 404.

### Errors

| HTTP | type | code | When |
|---|---|---|---|
| 400 | invalid_request_error | invalid_request_error | bad JSON/fields, blank query, count out of range, invalid URL, wrong Content-Type |
| 401 | authentication_error | invalid_request_error | missing/invalid Bearer (same wording as the reference) |
| 402 | insufficient_quota | insufficient_quota | daily quota exhausted (self-hosted extension) |
| 404 | not_found_error | invalid_request_error | any fetch failure: no content / upstream down / unsupported media type / SSRF block |
| 405 | — (empty body) | — | wrong method, `Allow: POST` |
| 413 | payload_too_large_error | invalid_request_error | request body or page too large |
| 429 | rate_limit_error | rate_limit_exceeded | RPM exceeded / global concurrency full |
| 500 / 504 | server_error / gateway_timeout_error | same as type | internal error / upstream timeout |

Envelope matches the reference: `{"error":{"message","type","param":null,"code"}}`, with
`(request_id: …)` appended to the message; 429/402 carry `Retry-After`, every response carries
`X-Request-Id` and CORS headers.

---

## Contract verified against the official API

On 2026-10-18 this project was compared against `api.deepseek.com` with a real key, one probe at
a time (60+ requests). Full matrix, raw findings and remaining deviations live in
**[docs/COMPAT.md](docs/COMPAT.md)**; every fact is pinned by the opt-in golden test
`internal/compat/official_test.go`. Condensed:

| Area | Measured on the official API | This project |
|---|---|---|
| Response fields | 10, order `title…content` | same order |
| `date` | `+00:00` offset, or `null` | same |
| `count` | 1–50, default 10; `50` → 50 results | same API bounds (fill depends on providers) |
| `query` | ≤128 runes (runes, not bytes) | same |
| `ori_question` | 1–128; empty string → 400 | same validation; unused for ranking |
| Auth order | 401 before any body validation | same |
| Content-Type | `application/json` required, else 400 | same |
| Body shape | must be a JSON object; `[]` / empty → 400 | same |
| `405` | empty body + `Allow: POST` | same |
| `401 / 413 / 504` | `authentication_error` / `payload_too_large_error` / `gateway_timeout_error` | same |
| `404` (fetch) | `failed to fetch URL` — also used for private/blocked targets | same |
| Fetch media types | text/plain, markdown, css, js, json accepted; xml/csv/pdf/images → 404 | same |
| `need_content` | fills **every** returned result | fills all (≤50), best effort |
| `snippet` / `summary` | 80–364 / 144–4386 runes | heuristic, caps aligned to the measured range |
| `rerank_score` | full-float, observed 0.10–2.29 | heuristic, 2 decimals — not comparable value-wise |
| Fetch redirects | 12 hops still 200 | `max_redirects: 15` |

---

## Differences from the official implementation

1. **Scores and summaries are not comparable.** `rerank_score` is a heuristic blend of RRF,
   authority and freshness; `authority` is a domain tier table; `summary` is content truncation
   or snippet joining. Field shapes and ranges match, but thresholds tuned for the reference
   (e.g. `score>=1.5`) will distribute differently.
2. **`after:` / `before:` are post-filters** and only apply when a result carries a date.
3. **`localization` has no effect** (no GeoIP).
4. **`ori_question` does not affect ranking** (no LLM enrichment in v1).
5. **Provider allowlists filter, they do not error**: with no usable provider you get `{"results":[]}`.
6. **`need_content` is best effort**: all results (cap 50) are fetched, but blocked/timed-out
   pages keep `content: ""` (the reference also skips unreachable sites).
7. **Gibberish queries may return fewer/zero results** because of off-topic filtering; the
   reference returns 10 low-score results.
8. **`site_icon` uses a local template** (`<host>/favicon.ico` by default). Set
   `site_icon_template` to `https://cdn.deepseek.com/site-icons/{host}` to mimic the reference.
9. **No JS rendering**: SPA pages fail more often than with the reference.

---

## Operations

- Health: `GET /healthz` (provider breaker states, unauthenticated)
- Metrics: `GET /metrics` (Prometheus text, unauthenticated — **do not expose publicly**)
- Logs: JSON by default with `request_id`, `key`, `endpoint`, `status`, `duration_ms`, `results`
- Cache: search 5 min, fetch 30 min, TTL-LRU + singleflight; quotas and cache reset on restart

```
onesearch_requests_total{endpoint,status}
onesearch_request_duration_seconds{endpoint}
onesearch_provider_requests_total{provider,outcome}  # ok/error/hedged/stale_dropped/rate_limited/breaker_open
onesearch_provider_breaker_state{provider}
onesearch_fetch_total{status}
onesearch_cache_hits_total{kind}
onesearch_rate_limited_total{reason}
onesearch_results_filtered_total
```

---

## Security & compliance

Before exposing publicly:

1. `auth.mode: key`, replace the example keys, tighten `allowed_providers`.
2. Put HTTPS in front, expose only `/v1/` and `/healthz`; keep `/metrics` internal.
3. Keep `fetch.allow_private: false`; run a separate instance if you truly need intranet fetches.
4. Configure `server.trusted_proxies`, otherwise `X-Forwarded-For` is ignored.
5. Tune `search.max_concurrency` and provider `rpm` so you don't hammer upstreams.

Known compliance notes:

- **Bing**: Microsoft's RSS copyright notice restricts results to personal, non-commercial use
  in an RSS aggregator; scraping HTML/RSS is also subject to their anti-bot measures and terms.
  Self-use is lower risk — **public hosting or commercial use needs your own legal review**.
- This service only behaves like a real browser with retries; it does not solve CAPTCHAs or
  bypass paywalls, and degrades when challenged.
- **DuckDuckGo** HTML parsing may violate their ToS and will hit CAPTCHAs at volume.
- **SearXNG** is AGPL software; self-hosting it is fine, but upstream engines may block its IP.

Security reports: please follow [SECURITY.md](SECURITY.md) (private, no public issues).

---

## Development

```bash
go vet ./...
go test ./...
go build -o onesearch ./cmd/onesearch

# Live network smoke tests (optional)
go test ./internal/provider -tags live -run TestLiveBingCN -v -count=1
ONESEARCH_LIVE_INTL=1 go test ./internal/provider -tags live -run TestLiveBingIntl -v -count=1  # run outside mainland China

# Official contract golden test (optional, spends a little official quota)
DEEPSEEK_API_KEY=sk-... go test ./internal/compat -tags official -v -count=1
```

Layout:

```
cmd/onesearch/            entrypoint
internal/api/             routing, middleware, handlers, /healthz, /metrics
internal/auth/            key auth, RPM, daily quota
internal/cache/           TTL-LRU + singleflight
internal/compat/          official-contract golden test (-tags official)
internal/config/          YAML + env + validation
internal/enrich/          snippet/summary/site/date enrichment
internal/extract/         HTML → title / text / Markdown
internal/fetchguard/      SSRF checks and safe dialer
internal/fetchsvc/        fetch orchestration and caching
internal/httpx/           wire types and error envelope
internal/limit/           token buckets, semaphore, circuit breaker
internal/obs/             logging and metrics
internal/provider/        search backends (Bing session/fingerprint, SearXNG, DDG, Brave, Tavily)
internal/query/           search syntax parser
internal/rank/            normalize, dedupe, RRF fusion, scoring
internal/search/          concurrent fan-out, hedge, content backfill
internal/siteinfo/        domain → authority / site name / favicon
docs/                     DESIGN, COMPAT (measured matrix), DSH integration
deploy/                   SearXNG settings, Windows start script
```

---

## Known limitations

- Bing occasionally soft-limits with `200 + empty channel` or replays another query's cache;
  retries, HTML fallback and batch filtering mitigate it, but results can still be a few short.
- Outside mainland China `cn.bing.com` redirects to the intl site; configure `www.bing.com`.
- DDG is unreachable on some networks; SearXNG requires your own instance.
- No JS rendering, no LLM summaries/semantic rerank, no GeoIP.
- Quotas and caches are in-memory; multiple instances count independently.
- No guarantee of following future DeepSeek schema changes (the golden test is how we notice).

## Roadmap

- LLM enrichment (summary rewriting / semantic rerank) behind a config switch
- Redis-backed shared cache and quotas
- Admin API: dynamic keys, usage queries
- More sources: Wikipedia, GitHub, arXiv, Hacker News
- Fetch retries and a JS-rendering fallback

## Related projects

- [DeepSeek web search & fetch docs](https://api-docs.deepseek.com/zh-cn/guides/search)
- [SearXNG](https://github.com/searxng/searxng) and its [Search API](https://docs.searxng.org/dev/search_api.html)
- [yokingma/one-search-mcp](https://github.com/yokingma/one-search-mcp) — multi-source search MCP (TS)
- [fastcrw/crw](https://github.com/fastcrw/crw) — Firecrawl/Tavily-compatible self-hosted fetch (Rust)
- [Ghilteras/searxng-gateway](https://github.com/Ghilteras/searxng-gateway) — SearXNG routing proxy + breaker (Go)
- [digitalninjanv/free-web-search](https://github.com/digitalninjanv/free-web-search) — free search backend chain with SSRF protection (Python)

## License

MIT

## Disclaimer

For personal study and self-use. The service behaves like a browser to aggregate publicly
available search results; it does **not** solve CAPTCHAs or bypass paywalls, but scraping is
still governed by each upstream's terms (see "Security & compliance"). Open-sourcing and
distribution grant no rights to any upstream service; users assess compliance risk themselves
and the maintainers accept no liability. Report security issues privately per
[SECURITY.md](SECURITY.md).
