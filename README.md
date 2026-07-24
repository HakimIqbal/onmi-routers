# onmi-routers

[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)
[![Upstreams](https://img.shields.io/badge/upstreams-Grok%20%7C%20CodeBuddy%20%7C%20Cloudflare-blue)](./)
[![Tests](https://img.shields.io/badge/tests-Passing-brightgreen)](./)

**Unified OpenAI-compatible API gateway** that fronts **Grok**, **CodeBuddy**, and **Cloudflare Workers AI** behind a single `/v1/chat/completions` endpoint — round-robin across thousands of upstream accounts, automatic key management, circuit breakers, per-key quotas, Redis hot-state, and full request logging.

> 📖 **Bilingual documentation** — This README is available in **English** and **Bahasa Indonesia**.
> 📖 **Dokumentasi bilingual** — README ini tersedia dalam **Bahasa Inggris** dan **Bahasa Indonesia**.

---

# 🇬🇧 English

## What is onmi-routers?

`onmi-routers` is a fork of [FoxRouters](https://github.com/rilspratama/Foxrouters) extended with **Cloudflare Workers AI** as a **third upstream**. One OpenAI-shaped endpoint, three backends, thousands of accounts:

| Prefix | Upstream | Endpoint |
|--------|----------|----------|
| `grok-*` | Grok | `https://cli-chat-proxy.grok.com` |
| `cb/*` | CodeBuddy | `https://www.codebuddy.ai/v2` |
| `cf/*` | **Cloudflare Workers AI** | `https://api.cloudflare.com/client/v4/accounts/{id}/ai/run/{model}` |

The Cloudflare adapter is purpose-built for the **12k-account farm** scenario: each account is a separate Cloudflare account with its own Account ID + API token (Bearer). It fronts the Workers AI `ai/run/{model}` endpoint, translates OpenAI `chat/completions` ↔ Workers AI payloads (streaming + non-streaming), and rotates accounts with weighted round-robin by remaining daily quota.

### Why add Cloudflare?

- **Free daily Workers AI quota** per account — multiply it across thousands of accounts.
- **Static tokens** — no OAuth refresh loop (unlike Grok/CodeBuddy OAuth).
- **Anti-correlation** — sticky per-account HTTP/SOCKS5 proxy support prevents Cloudflare from linking all traffic to one egress IP (which would trigger mass-disable).

## Features

- **Three-upstream model-prefix routing** — `grok-*` → Grok, `cb/*` → CodeBuddy, `cf/*` → Cloudflare Workers AI.
- **Cloudflare account pool** — weighted round-robin by remaining daily quota, thousands of accounts, hot-loaded from Redis.
- **Smart 429 handling** — two cases:
  - Rate-limit **burst** (has `Retry-After`) → short cooldown + retry other accounts.
  - **Daily quota exhausted** (no `Retry-After`) → skip until next UTC midnight.
- **401 / 403 → permanent disable** — dead/invalid tokens or banned accounts are coreted (never retried) and cleaned up via the dashboard.
- **Sticky per-account proxy** — scope the proxy pool to `cloudflare` for anti-correlation (recommended for large farms).
- **Grok alias expansion** — `grok-4.5-{high,medium,low,xhigh,auto,none}` → `grok-4.5` + injected `reasoning_effort`.
- **Multi-account / multi-key round-robin** — O(k) `Next()` on the hot path; background workers handle re-enable + refresh.
- **Auto token refresh** (Grok/CodeBuddy) — singleflight-guarded, pre-warmed on a 30 s tick.
- **Circuit breaker** — passive (401/403/credit/quota disable + Redis persist) + active health checks (~10 min).
- **Custom models + aliases** — runtime-configurable model aliases backed by Redis, no restart.
- **Combos** — group N models under `combo/<name>` with `fallback` or `round_robin` strategy.
- **Proxy pool manager** — dashboard-managed HTTP/SOCKS5 pool, per-upstream scoping, auto-disable after 5 failures.
- **Per-gateway-key** RPM, burst, token quota, model whitelist, role (`admin` vs `inference`).
- **Redis** hot state + **ClickHouse / SQLite** cold full-body history.
- **Embedded web dashboard** — stats, accounts (Grok / CodeBuddy / **Cloudflare**), keys, models, proxies, tunnel.

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

Requirements: Go 1.25+, Redis (hot state), optional ClickHouse (cold log).

## Using Cloudflare (`cf/*`)

### 1. Import accounts

```bash
# Single
curl -X POST http://localhost:20130/cf/import \
  -H "Content-Type: application/json" \
  -d '{"account_id":"your-cf-account-id","token":"your-cf-api-token"}'

# Bulk (one per line: account_id:token)
curl -X POST http://localhost:20130/cf/import/bulk \
  -H "Content-Type: application/json" \
  -d '{"raw":"acct-1:token-1\nacct-2:token-2"}'
```

### 2. Chat via the gateway

```bash
curl -X POST http://localhost:20130/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "cf/llama-8b",
    "messages": [{"role":"user","content":"Hello"}],
    "stream": true
  }'
```

Supported `cf/*` aliases: `cf/llama-70b`, `cf/llama-8b`, `cf/deepseek-r1`, `cf/qwen-32b`, `cf/llama-3.3-70b`, and any raw `@cf/...` Workers AI model slug.

### 3. Manage in the dashboard

Open `/dashboard` → **Cloudflare** tab → add / bulk-import / delete accounts, view quota + disabled status, cleanup disabled.

## API Reference

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/chat/completions` | POST | Main proxy (route by `model` prefix) |
| `/v1/models` | GET | List models (includes `cf/*` aliases) |
| `/health` | GET | Public health + upstream stats |
| `/cf/import` | POST | Import single CF account |
| `/cf/import/bulk` | POST | Bulk import CF accounts (`raw` field) |
| `/cf/keys/:account` | DELETE | Delete a CF account |
| `/cf-stats` | GET | CF pool stats (per-account) |
| `/cleanup/disabled?type=cf` | POST | Permanently delete disabled CF accounts |

## Configuration (essentials)

```
REDIS_ADDR / REDIS_PASSWORD / REDIS_DB
LOG_BACKEND=sqlite            # sqlite (default) | clickhouse
LOG_SQLITE_PATH=/var/lib/foxrouters/logs.db
PORT=20130
GATEWAY_KEY_FILE / CB_KEY_FILE
CF_PROXY_SCOPE=cloudflare     # assign proxies to CF for anti-correlation
```

## License

MIT — see [LICENSE](./LICENSE).

---

# 🇮🇩 Bahasa Indonesia

## Apa itu onmi-routers?

`onmi-routers` adalah fork dari [FoxRouters](https://github.com/rilspratama/Foxrouters) yang diperluas dengan **Cloudflare Workers AI** sebagai **upstream ketiga**. Satu endpoint berbentuk OpenAI, tiga backend, ribuan akun:

| Prefiks | Upstream | Endpoint |
|---------|----------|----------|
| `grok-*` | Grok | `https://cli-chat-proxy.grok.com` |
| `cb/*` | CodeBuddy | `https://www.codebuddy.ai/v2` |
| `cf/*` | **Cloudflare Workers AI** | `https://api.cloudflare.com/client/v4/accounts/{id}/ai/run/{model}` |

Adapter Cloudflare dibuat khusus untuk skenario **farm 12k akun**: setiap akun adalah akun Cloudflare terpisah dengan Account ID + API token (Bearer) sendiri. Adapter ini menempel ke endpoint Workers AI `ai/run/{model}`, menerjemahkan payload OpenAI `chat/completions` ↔ Workers AI (streaming + non-streaming), dan merotasi akun dengan weighted round-robin berdasarkan sisa kuota harian.

### Kenapa tambah Cloudflare?

- **Kuota Workers AI harian gratis** per akun — kalikan across ribuan akun.
- **Token statis** — tidak ada loop refresh OAuth (berbeda dengan Grok/CodeBuddy OAuth).
- **Anti-korelasi** — dukungan proxy HTTP/SOCKS5 sticky per-akun mencegah Cloudflare menghubungkan seluruh traffic ke satu IP egress (yang bisa memicu mass-disable).

## Fitur

- **Routing prefiks model tiga-upstream** — `grok-*` → Grok, `cb/*` → CodeBuddy, `cf/*` → Cloudflare Workers AI.
- **Pool akun Cloudflare** — weighted round-robin by sisa kuota harian, ribuan akun, hot-loaded dari Redis.
- **Penanganan 429 pintar** — dua kasus:
  - **Burst rate-limit** (ada `Retry-After`) → cooldown singkat + retry akun lain.
  - **Kuota harian habis** (tanpa `Retry-After`) → skip sampai tengah malam UTC berikutnya.
- **401 / 403 → disable permanen** — token mati/invalid atau akun dibanned di-coret (tidak pernah di-retry) dan dibersihkan lewat dashboard.
- **Proxy sticky per-akun** — scope proxy pool ke `cloudflare` untuk anti-korelasi (direkomendasikan untuk farm besar).
- **Ekspansi alias Grok** — `grok-4.5-{high,medium,low,xhigh,auto,none}` → `grok-4.5` + `reasoning_effort` yang disuntikkan.
- **Multi-akun / multi-key round-robin** — O(k) `Next()` di hot path; background worker menangani re-enable + refresh.
- **Auto refresh token** (Grok/CodeBuddy) — singleflight-guarded, pre-warm tiap 30 detik.
- **Circuit breaker** — pasif (disable 401/403/credit/quota + persist Redis) + health check aktif (~10 menit).
- **Custom model + alias** — alias model bisa diubah saat runtime via Redis, tanpa restart.
- **Combos** — gabungkan N model di bawah `combo/<nama>` dengan strategi `fallback` atau `round_robin`.
- **Proxy pool manager** — pool HTTP/SOCKS5 yang dikelola dashboard, scoping per-upstream, auto-disable setelah 5 kegagalan.
- **Per-gateway-key** RPM, burst, kuota token, whitelist model, role (`admin` vs `inference`).
- **Redis** hot-state + **ClickHouse / SQLite** cold full-body history.
- **Web dashboard tertanam** — stats, akun (Grok / CodeBuddy / **Cloudflare**), keys, models, proxies, tunnel.

## Mulai Cepat

### Docker (rekomendasi)

```bash
git clone https://github.com/HakimIqbal/onmi-routers
cd onmi-routers
cp .gateway.env.example .gateway.env   # isi secrets
docker compose up -d --build
curl -s http://127.0.0.1:20130/health
```

### Dari source

```bash
export PATH=$PATH:/usr/local/go/bin
go build -o onmi-routers .
REDIS_ADDR=127.0.0.1:6379 ./onmi-routers
```

Persyaratan: Go 1.25+, Redis (hot state), opsional ClickHouse (cold log).

## Menggunakan Cloudflare (`cf/*`)

### 1. Import akun

```bash
# Satuan
curl -X POST http://localhost:20130/cf/import \
  -H "Content-Type: application/json" \
  -d '{"account_id":"cf-account-id-kamu","token":"cf-api-token-kamu"}'

# Bulk (satu per baris: account_id:token)
curl -X POST http://localhost:20130/cf/import/bulk \
  -H "Content-Type: application/json" \
  -d '{"raw":"acct-1:token-1\nacct-2:token-2"}'
```

### 2. Chat lewat gateway

```bash
curl -X POST http://localhost:20130/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "cf/llama-8b",
    "messages": [{"role":"user","content":"Halo"}],
    "stream": true
  }'
```

Alias `cf/*` yang didukung: `cf/llama-70b`, `cf/llama-8b`, `cf/deepseek-r1`, `cf/qwen-32b`, `cf/llama-3.3-70b`, dan slug model Workers AI `@cf/...` apa pun.

### 3. Kelola di dashboard

Buka `/dashboard` → tab **Cloudflare** → tambah / bulk-import / hapus akun, lihat kuota + status disabled, cleanup disabled.

## Referensi API

| Endpoint | Method | Fungsi |
|----------|--------|--------|
| `/v1/chat/completions` | POST | Proxy utama (route by prefiks `model`) |
| `/v1/models` | GET | Daftar model (termasuk alias `cf/*`) |
| `/health` | GET | Health publik + stats upstream |
| `/cf/import` | POST | Import 1 akun CF |
| `/cf/import/bulk` | POST | Bulk import akun CF (field `raw`) |
| `/cf/keys/:account` | DELETE | Hapus akun CF |
| `/cf-stats` | GET | Stats pool CF (per-akun) |
| `/cleanup/disabled?type=cf` | POST | Hapus permanen akun CF yang disabled |

## Konfigurasi (inti)

```
REDIS_ADDR / REDIS_PASSWORD / REDIS_DB
LOG_BACKEND=sqlite            # sqlite (default) | clickhouse
LOG_SQLITE_PATH=/var/lib/foxrouters/logs.db
PORT=20130
GATEWAY_KEY_FILE / CB_KEY_FILE
CF_PROXY_SCOPE=cloudflare     # pasang proxy ke CF untuk anti-korelasi
```

## Lisensi

MIT — lihat [LICENSE](./LICENSE).

---

<p align="center">
  <sub>onmi-routers · fork of FoxRouters + Cloudflare Workers AI upstream · Made with ☕ by HakimIqbal</sub>
</p>
