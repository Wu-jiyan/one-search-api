# 接入 DeepSeek Harness（DSH）

把 one-search-api 作为 DSH 的 `web_search` / `web_fetch` 后端。DSH 内置
`@deepseek-ai/dsh-web-deepseek-api` 插件，注册 `deepseek-search-api` 与
`deepseek-fetch-api` 两个提供方，只需要改一个 `baseURL` 就指向本服务——
本项目的契约就是照着这个 API 对齐的（见 [COMPAT.md](COMPAT.md)）。

## 1. 启动服务

```powershell
# 构建（已构建过可跳过）
go build -o onesearch.exe ./cmd/onesearch

# 启动（配置里监听 127.0.0.1:8080）
.\deploy\windows\start-onesearch.ps1

# 自检
curl.exe -s http://127.0.0.1:8080/healthz
```

`config.yaml` 已被 `.gitignore` 忽略：里面有本机使用的 API Key、监听地址和
provider 选择。示例配置见 `config.example.yaml`。

## 2. 配置 DSH

编辑 `%USERPROFILE%\.dsh\profiles\desktop\cordis.patch.yml`（先备份），两处：

```yaml
# 1) 把 web 服务切到这两个提供方（config 是整对象替换，两个字段都要写）
- id: web
  config:
    searchProvider: deepseek-search-api
    fetchProvider: deepseek-fetch-api

# 2) 指向本服务；apiKey 与 config.yaml 里 auth.keys[].token 必须一致
- id: web-deepseek-api
  name: "@deepseek-ai/dsh-web-deepseek-api"
  config:
    apiKey: "sk-onesearch-..."
    baseURL: "http://127.0.0.1:8080/v1/tools"
    count: 10
    sendOriginalQuestion: true
```

DSH 会热加载 profile patch，**一般不需要重启**。改动后在下一次 `web_search`
就能生效；也可以到 设置 → 插件 → 内置插件配置 → 网页搜索 里确认开关状态。

字段含义（来自 DSH 包文档）：

| 字段 | 默认 | 说明 |
|---|---|---|
| `baseURL` | `https://api.deepseek.com/v1/tools` | 本服务填 `http://127.0.0.1:8080/v1/tools`，DSH 会拼接 `/search`、`/fetch` |
| `apiKey` | 无 | 明文 Key，优先于 `apiKeyEnv` |
| `count` | `10` | 每次搜索条数，1–50 |
| `sendOriginalQuestion` | `true` | 把触发搜索的用户原话（截断 128 字）作为 `ori_question` 发过来 |

## 3. 验证

服务端日志（`onesearch.log`）里应能看到来自 DSH 的请求：

```
msg=request endpoint=search method=POST path=/v1/tools/search status=200 duration_ms=788 key=dsh results=8
msg=request endpoint=fetch  method=POST path=/v1/tools/fetch  status=200 duration_ms=881 key=dsh
```

DSH 侧调用一次 `web_search`，日志出现对应请求即接入成功。

## 4. 回滚

```yaml
- id: web
  config:
    searchProvider: bing-rss          # 或原来的值
    fetchProvider: web-search-free
```

再删除 `web-deepseek-api` 覆盖节并重启 DSH。备份文件形如
`cordis.patch.yml.bak-before-onesearch-<时间戳>`。

## 5. 已知差异与注意

- **DSH 只消费 `snippet` 和 `date`**，不读 `summary` / `site_name` / `site_icon` / `rerank_score`；
  所以本项目的启发式分数不会影响 DSH 的展示。
- `ori_question` 会照发，但本服务 v1 不参与排序（见 COMPAT §9）。
- `count` 固定为 10；纯 Bing 部署下 Bing 每页只有 10 条，更多结果需要启用 SearXNG/Brave。
- `fetchProvider` 换成 `deepseek-fetch-api` 后，抓取能力等于本服务（readability + 静态 HTTP，
  无 JS 渲染）；抓不到的站点会报 `WEB_PROVIDER_ERROR` 而不是回退到 jina。
- 服务必须先启动，否则 DSH 的 `web_search`/`web_fetch` 会直接失败（DSH 不做 provider 回退）。
