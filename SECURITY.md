# Security Policy

## 支持的版本

项目处于 `0.x`，只有 `main` 分支最新提交接受修复。

## 报告安全问题

**不要开公开 issue。** 请通过以下任一方式私下联系维护者：

- GitHub Security Advisories：仓库 **Security → Report a vulnerability**
- 或发邮件给仓库 owner（见 GitHub 个人主页）

请在报告里包含：受影响的提交/版本、复现步骤、影响范围（例如能否绕过鉴权、SSRF、命令执行、信息泄露）。
一般 7 天内回复；修复发布后会在 Release / Advisory 里致谢（除非你要求匿名）。

## 安全边界（设计上已考虑的部分）

- **SSRF**：只允许 http/https、禁止 userinfo、端口白名单、解析后 IP 段校验（含 loopback / 私网 /
  link-local / 云 metadata）、自定义 Dialer 用已校验 IP 直连（防 DNS rebinding）、逐跳重定向复检。
  默认 `fetch.allow_private: false`，**不要为了抓内网页面在公网实例上打开**。
- **鉴权**：Bearer Key，支持 `sha256:<hex>` 存储，常数时间比较；每 Key RPM / 每日配额 / provider 白名单。
- **反滥用**：请求体上限、字段长度校验、全局并发闸门、provider 熔断与限速。
- **`/metrics` 无鉴权**，只应暴露在内网；公网部署请只放行 `/v1/` 与 `/healthz`。

## 不属于安全问题的部分

- 上游搜索引擎变更、反爬策略升级导致的结果变少或抓取失败。
- 因违反上游服务条款而产生的账号/网络处置风险（见 README「免责声明」）。
- 本项目**不**实现验证码破解、付费墙绕过或登录态复用。

## 部署建议

1. `auth.mode: key`，替换掉示例 Key。
2. 前置 HTTPS 反代，只暴露 `/v1/`、`/healthz`。
3. 配置 `server.trusted_proxies`，否则 `X-Forwarded-For` 不被采信（防绕过按 IP 限流）。
4. 保持 `fetch.allow_private: false`。
5. `/metrics` 走内网或加访问控制。
