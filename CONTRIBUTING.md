# Contributing

[English](#english) | [中文](#中文)

## English

Thanks for taking a look. This project is small and the scope is deliberately narrow: a
self-hosted, contract-compatible replacement for DeepSeek's Search / Fetch API.

### Scope

**In scope**

- Wire-contract fixes backed by measurement against the reference API (`docs/COMPAT.md`)
- New search backends that are free/keyless or have a generous free tier
- Robustness: anti-bot behavior, retries, caching, rate limiting, SSRF hardening
- Bug fixes, documentation, tests

**Out of scope**

- Captcha solving, paywall/login bypass, or anything that circumvents an upstream's access control
- Billing, accounts, multi-tenant dashboards (the project targets self-use plus a few friends)
- Client-side UI work; this is a service

### Development

```bash
go build ./...
go vet ./...
go test ./...

# optional: real-network smoke tests
go test ./internal/provider -tags live -run TestLiveBingCN -v -count=1

# optional: reference-API golden test (spends a little official quota)
DEEPSEEK_API_KEY=sk-... go test ./internal/compat -tags official -v -count=1
```

Requirements: Go 1.27+. No code generation, no Node toolchain.

### Before opening a PR

1. `gofmt -l .` prints nothing; `go vet ./...` and `go test ./...` pass.
2. New behavior has a test. Contract changes must also reference the measured evidence.
3. Keep provider-specific quirks inside `internal/provider/`; keep the wire layer in `internal/httpx/`.
4. Do not add a dependency unless the stdlib really cannot do it — small modules, few deps.
5. Update `docs/COMPAT.md` / `README.md` when behavior or the contract changes.

### Commits

Conventional-ish prefixes: `feat:`, `fix:`, `docs:`, `chore:`, `test:`. One logical change per
commit. English or Chinese is fine.

### Security

Report vulnerabilities privately per [SECURITY.md](SECURITY.md) instead of opening an issue.

---

## 中文

### 范围

**欢迎**

- 有实测依据的契约修正（对照官方 API，证据写进 `docs/COMPAT.md`）
- 新的免费/无需 Key 的搜索源接入
- 稳定性：反爬、重试、缓存、限流、SSRF 加固
- Bug 修复、文档、测试

**不接受**

- 破解验证码、绕过付费墙/登录态，或任何规避上游访问控制的功能
- 计费、账号体系、多租户后台（定位是自用 + 少数朋友）
- 客户端 UI（这是服务端项目）

### 开发

```bash
go build ./...
go vet ./...
go test ./...

# 可选：真实网络冒烟
go test ./internal/provider -tags live -run TestLiveBingCN -v -count=1

# 可选：官方契约对照（会消耗少量官方额度）
DEEPSEEK_API_KEY=sk-... go test ./internal/compat -tags official -v -count=1
```

需要 Go 1.27+；不需要代码生成，也不需要 Node 工具链。

### 提 PR 前

1. `gofmt -l .` 无输出；`go vet ./...` 与 `go test ./...` 通过。
2. 新行为要有测试；契约相关改动要附实测依据。
3. 源特有的怪癖留在 `internal/provider/`，线协议留在 `internal/httpx/`。
4. 能不用第三方库就不用——项目刻意保持小依赖。
5. 行为或契约变化时同步更新 `docs/COMPAT.md` / `README.md`。

### 提交

建议用 `feat:` / `fix:` / `docs:` / `chore:` / `test:` 前缀，一个提交一件事。中英文都行。

### 安全

漏洞请按 [SECURITY.md](SECURITY.md) 私下报告，不要开公开 issue。
