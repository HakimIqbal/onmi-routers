# onmi-routers

[![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)
[![Upstreams](https://img.shields.io/badge/upstreams-Grok%20%7C%20CodeBuddy%20%7C%20Cloudflare-blue)](./)
[![Dashboard](https://img.shields.io/badge/dashboard-embedded%20SPA-8B5CF6)](./)

**Unified OpenAI-compatible API gateway** that fronts **Grok**, **CodeBuddy**, and **Cloudflare Workers AI** behind a single `/v1/chat/completions` endpoint — weighted round-robin across thousands of upstream accounts, automatic token lifecycle management, circuit breakers, per-key quotas, token compression, live analytics, and an embedded full-featured dashboard.

> 📖 **Bilingual documentation** — English and Bahasa Indonesia below.

---

# 🇬🇧 English

## What is onmi-routers?

`onmi-routers` is a fork of [FoxRouters](https://github.com/rilspratama/Foxrouters) extended with **Cloudflare Workers AI** as a third upstream, a full **ops surface** (analytics, compression, console, CLI tooling), and production-grade **security hardening**. One OpenAI-shaped endpoint, three backends, thousands of accounts:

| Prefix | Upstream | Endpoint |
|--------|----------|----------|
| `grok-*` | Grok (xAI) | `https://cli-chat-proxy.grok.com` |
| `cb/*` | CodeBuddy | `https://www.codebuddy.ai/v2` |
| `cf/*` / `@cf/*` | Cloudflare Workers AI | `https://api.cloudflare.com/client/v4/accounts/{id}/ai/run/{model}` |

## Architecture

```
Client → AuthMiddleware (Bearer / session cookie)
       → RateLimitMiddleware (per-key RPM + burst)
       → CSRF Guard (cookie-auth mutations)
       → /v1/chat/completions | /v1/embeddings | /v1/models
            ↓
       proxyRequest (model-prefix routing + alias expansion)
       ├── grok-*  → proxyGrok   (O(k) RR, 401 retry, 403 ban + Redis persist)
       ├── cb/*    → proxyCodeBuddy (dual pool: api_key + OAuth, credit sync)
       └── cf/*    → proxyCloudflare (weighted RR by quota, neurons billing)
            ↓
       Token Compression (optional: RTK / Caveman / Ultra / Summarizer)
            ↓
       async LogRequest → SQLite (default) or ClickHouse (full body, ZSTD)
```

### Data stores

| Layer | Engine | Purpose |
|-------|--------|---------|
| Hot | **Redis** | Tokens, credits, disabled flags, gateway keys, rate state, proxy pool, combos, custom models |
| Cold | **SQLite** (default) or **ClickHouse** | Full request/response logs, per-model stats, cost tracking |

## Features

### Core Gateway

- **Three-upstream model-prefix routing** — `grok-*` → Grok, `cb/*` → CodeBuddy, `cf/*` → Cloudflare Workers AI.
- **Multi-account round-robin** — O(k) `Next()` on the hot path; background workers handle re-enable + token refresh. Zero network calls under account mutex.
- **Auto token refresh** (Grok/CodeBuddy) — singleflight-guarded, pre-warmed on a 30s tick, 30-min expiry window.
- **Circuit breaker** — passive (401/403/credit/quota disable + Redis persist) + active health probes (~10 min interval).
- **Smart 429 handling** — burst (has `Retry-After`) → short cooldown + retry; daily quota exhausted → skip until next UTC midnight.
- **Grok alias expansion** — `grok-4.5-{high,medium,low,xhigh,auto,none}` → `grok-4.5` + injected `reasoning_effort`.
- **Cloudflare account pool** — weighted round-robin by remaining daily quota, hot-loaded from Redis, designed for 12k+ account farms.
- **CB dual pool** — API key (`ck_*`) + OAuth accounts in one round-robin pool, meter-based credit sync every 5 min.
- **Cost tracking** — per-request USD estimation (token-priced models + CF neurons billing at $0.011/1M neurons).

### Combos (17 strategies)

Group N models under `combo/<name>` with intelligent routing:

| Category | Strategies |
|----------|-----------|
| Basic | `fallback`, `round_robin`, `random`, `priority`, `fill_first` |
| Smart | `auto` (3-tier: Subscription → Cheap → Free), `least_used` |
| Performance | `latency`, `throughput`, `success_rate` |
| Cost | `cost`, `cost_latency_balanced` |

Self-healer skips OPEN circuits on smart strategies; heals every 30s.

### Token Compression (OmniRoute-style)

Five compression modes wired into the proxy hot path:

| Mode | Engine | Behavior |
|------|--------|----------|
| `off` | — | Pass-through (default) |
| `lite` | RTK | Near-lossless filler/whitespace cleanup |
| `standard` | Caveman | Aggressive prose shortening |
| `aggressive` | Tool-result | Compress tool outputs, preserve system prompt |
| `ultra` | Heuristic pruner | Maximum compression (lossy) |

Fidelity gate rejects lossy results that save <3%. Cache-aware mode downgrades for caching providers. **Compression Studio** in the dashboard lets you test all engines side-by-side with diff + waterfall visualization.

### Ops Surface (Dashboard)

Embedded single-page dashboard (`/dashboard`) with 11 views:

| View | What it does |
|------|-------------|
| **Dashboard** | 12 stat cards (4×3 grid), upstream health table, quick test, request history |
| **Providers** | Grok / CodeBuddy / Cloudflare account pools — import, bulk import, cleanup, pagination |
| **Combos** | Create/delete combos with 17 strategies, model chip selector |
| **Models** | Full catalog grouped by provider, per-model usage stats, custom aliases, hide/restore |
| **Usage** | Analytics: stat cards, **live routing topology** (electric animated edges), recent requests, usage-over-time chart, per-model cost/token breakdown |
| **Token Saver** | Compression mode selector, engine toggles, Compression Studio playground |
| **CLI Tools** | One-click config generator for Claude Code, Codex, Cursor, Cline, OpenClaw, Continue, Roo Code, GitHub Copilot |
| **Proxy Pools** | HTTP/SOCKS5 proxy manager, per-upstream scoping, auto-disable after 5 failures |
| **Console Log** | Real-time SSE log stream with level filters + auto-scroll |
| **Remote** | Cloudflare Tunnel control (quick / named / hybrid), Tailscale status |
| **Endpoint & Key** | API endpoint display, gateway key CRUD with per-key RPM/quota/role |

### Routing Topology (9router-style)

Live node graph on the Usage page:
- Provider nodes activate when they serve traffic within the last 60 seconds.
- Active edges render with **electric animation** — cyan halo, green plasma, dashed core, energy particles + sparkle trail (SVG `animateMotion`).
- Router hub shows a yellow glow + active-count badge during live traffic.
- Nodes auto-dim back to idle once traffic stops (5s tick pruning).

### Security Hardening

- **Log redaction** — custom Gin logger strips `?key=...` from request paths (no gateway key leak in pm2 logs).
- **Cookie security** — `HttpOnly; Secure; SameSite=Lax` on session cookies.
- **Redis auth** — `requirepass` enforced; gateway reads via `REDIS_PASSWORD` env.
- **CSP** — `default-src 'self'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'` + `X-Frame-Options: DENY` + `X-Content-Type-Options: nosniff`.
- **CSRF guard** — Origin/Referer check on cookie-authenticated mutations.
- **Login rate limiting** — 5/min + 20/hour per IP.
- **Auth coverage** — all admin endpoints return 401 without Bearer; only `/dashboard` and `/health` are public.
- **No key in HTML** — dashboard.html contains zero gateway key values.

### Additional Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/chat/completions` | POST | Main proxy (route by model prefix) |
| `/v1/models` | GET | List models (includes `cf/*` aliases + combos) |
| `/v1/embeddings` | POST | Real CF BGE embeddings (768-dim) |
| `/health` | GET | Public health + upstream circuit stats |
| `/history` | GET | Aggregated stats (`?hours=N`), per-model breakdown |
| `/history/recent` | GET | Recent request previews |
| `/history/detail/:id` | GET | Full request/response JSON |
| `/api/keys` | GET/POST | Gateway key CRUD |
| `/api/combos` | GET/POST | Combo CRUD |
| `/api/proxies` | GET/POST | Proxy pool CRUD + test + toggle |
| `/api/tokensaver` | GET/POST | Compression mode config + analytics |
| `/api/tunnel/status` | GET | Cloudflare Tunnel status |
| `/api/tailscale/status` | GET | Tailscale status |
| `/console/stream` | GET | SSE log stream |
| `/mcp/tools` | GET | 12 MCP tools (evals, cache, quota, cost) |
| `/cf/import` | POST | Import CF account(s) |
| `/cb/import` | POST | Import CodeBuddy API key |
| `/cb/oauth/import` | POST | Import CodeBuddy OAuth account |
| `/accounts/import` | POST | Import Grok account |

## Quick Start

### Docker (recommended)

```bash
git clone https://github.com/HakimIqbal/onmi-routers
cd onmi-routers
cp .gateway.env.example .gateway.env   # fill secrets
docker compose up -d --build
curl -s http://127.0.0.1:20130/health
```

### From source

```bash
export PATH=$PATH:/usr/local/go/bin
go build -o onmi-routers .
REDIS_ADDR=127.0.0.1:6379 ./onmi-routers
```

Requirements: Go 1.25+, Redis (hot state). Optional: ClickHouse (cold log at scale).

### Cross-compile (deploy to x86_64 VPS from ARM)

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o onmi-routers-amd64 .
file onmi-routers-amd64   # must say "x86-64"
```

## Configuration

```env
REDIS_ADDR=127.0.0.1:6379
REDIS_PASSWORD=<your-redis-password>
REDIS_DB=0
LOG_BACKEND=sqlite                          # sqlite (default) | clickhouse
LOG_SQLITE_PATH=/var/lib/onmi-routers/logs.db
PORT=20130
COOKIE_SECURE=1                             # HTTPS-only session cookies
GATEWAY_KEY_FILE=gateway-key.txt
CB_KEY_FILE=codebuddy-key.txt
```

## License

MIT — see [LICENSE](./LICENSE).

---

# 🇮🇩 Bahasa Indonesia

## Apa itu onmi-routers?

`onmi-routers` adalah fork dari [FoxRouters](https://github.com/rilspratama/Foxrouters) yang diperluas dengan **Cloudflare Workers AI** sebagai upstream ketiga, **ops surface** lengkap (analytics, kompresi, console, CLI tooling), dan **security hardening** production-grade. Satu endpoint berbentuk OpenAI, tiga backend, ribuan akun:

| Prefiks | Upstream | Endpoint |
|---------|----------|----------|
| `grok-*` | Grok (xAI) | `https://cli-chat-proxy.grok.com` |
| `cb/*` | CodeBuddy | `https://www.codebuddy.ai/v2` |
| `cf/*` / `@cf/*` | Cloudflare Workers AI | `https://api.cloudflare.com/client/v4/accounts/{id}/ai/run/{model}` |

## Fitur Utama

- **Routing tiga upstream** — `grok-*` → Grok, `cb/*` → CodeBuddy, `cf/*` → Cloudflare Workers AI.
- **Round-robin multi-akun** — O(k) `Next()` di hot path, zero network call di bawah mutex.
- **Auto refresh token** — singleflight, pre-warm tiap 30 detik.
- **Circuit breaker** — pasif (401/403/credit/quota) + health probe aktif (~10 menit).
- **Pool Cloudflare 12k+ akun** — weighted RR by sisa kuota harian, hot-loaded dari Redis.
- **CB dual pool** — API key + OAuth dalam satu pool, credit sync tiap 5 menit.
- **Cost tracking** — estimasi USD per request (token + neurons billing CF).
- **17 strategi combo** — fallback, round_robin, latency, cost, auto, random, least_used, fill_first, priority, throughput, success_rate, cost_latency_balanced, dll.
- **Token Compression** — 5 mode (off/lite/standard/aggressive/ultra), fidelity gate, Compression Studio.
- **Dashboard 11 halaman** — stats, providers, combos, models, usage analytics, token saver, CLI tools, proxy pools, console log, remote tunnel, endpoint & keys.
- **Routing Topology live** — graph animasi electric (partikel + glow) yang menunjukkan traffic real-time.
- **Security hardening** — log redaction, cookie secure, Redis auth, CSP, CSRF guard, rate limiting.

## Mulai Cepat

```bash
git clone https://github.com/HakimIqbal/onmi-routers
cd onmi-routers
cp .gateway.env.example .gateway.env   # isi secrets
docker compose up -d --build
curl -s http://127.0.0.1:20130/health
```

Persyaratan: Go 1.25+, Redis. Opsional: ClickHouse (cold log skala besar).

## Lisensi

MIT — lihat [LICENSE](./LICENSE).

---

<p align="center">
  <sub>onmi-routers · fork of FoxRouters + Cloudflare Workers AI + full ops surface · Made with ☕ by HakimIqbal</sub>
</p>
