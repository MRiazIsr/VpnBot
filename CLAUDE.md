# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run Commands

```bash
# Backend (Go)
go build -o vpnbot .           # Build binary
go vet ./...                   # Lint/vet
go test ./service/ -v          # Unit tests (config generation, link generation)
go mod tidy                    # Sync dependencies

# Frontend (Next.js admin panel)
cd vpn-admin-panel
npm run dev                    # Dev server on :3000
npm run build                  # Production build
npm run lint                   # ESLint
```

CI runs `go vet` and `go build` on PRs (Go 1.21). Deploy to Hetzner triggers on push to main.

## Architecture

VPN management system: Go backend + Telegram bot + Next.js admin panel.

**Request flow:** `gin router` → `JWT middleware` → `handlers` → `database (GORM/SQLite)` + `service (sing-box config generation)`

### Packages

- **`database/`** — GORM models (`User`, `InboundConfig`, `ConnectionLog`), SQLite init with auto-migration and seed data
- **`service/`** — sing-box JSON config generation (`GenerateAndReload()`), subscription link generation (`GenerateLinkForInbound()`), traffic tracking via gRPC V2Ray Stats API, Hetzner Cloud Firewall (`firewall.go`), RuVDS iptables port forwarding via SSH (`portforward.go` — legacy, not active on prod), connectivity checks (`network.go`)
- **`api/handlers/`** — REST handlers: auth, users, inbounds CRUD, stats, public subscription endpoints
- **`api/middleware/`** — CORS and JWT Bearer auth
- **`api/router/`** — Route registration under `/api` with auth group
- **`bot/`** — Telegram bot (telebot.v3) with dynamic connection buttons from DB, QR codes

### Key data flow

1. `InboundConfig` records in DB define VPN inbounds (vless/hysteria2/shadowtls, ports, TLS, transport, Reality keys, ShadowTLS secrets)
2. `buildSingBoxConfig()` (pure function) builds sing-box JSON from `[]InboundConfig` + `[]User` + optional extra outbound + `route.final` tag. `GenerateAndReload()` is the I/O wrapper — reads DB + env, calls builder, writes `/etc/sing-box/config.json`, calls `ReloadService()`.
3. `buildInboundGroup()` returns `[]any` per DB inbound: 1 element for vless/hysteria2, 2 elements for shadowtls (paired outer `shadowtls` + inner `shadowsocks` on loopback).
4. Subscription endpoint (`/sub/:token`) calls `GenerateLinkForInbound()` for each enabled inbound → returns base64-encoded links. Format: `vless://...#tag` for VLESS, `sing-box://<base64 JSON bundle>` for ShadowTLS.
5. Bot dynamically generates connection buttons from enabled InboundConfig records in DB.

### InboundConfig model

Drives both sing-box config generation and subscription links. Key fields:

- `Protocol`: `"vless"` | `"hysteria2"` | `"shadowtls"`
- `Tag`, `DisplayName`: user-facing labels (visible as connection name in VPN clients)
- `ListenPort`, `SNI`, `ServerAddress`: connection endpoint (`ServerAddress` empty = fall back to `SERVER_IP`)
- `TLSType`: `"reality"` (uses per-inbound Reality keys) | `"certificate"` (uses cert_path/key_path) — applies to VLESS
- `Transport`: `""` (TCP) | `"http"` | `"grpc"` | `"httpupgrade"` | `"xhttp"` | `"ws"`
- `UserType`: `"legacy"` (with flow) | `"new"` (no flow) | `"hy2"` (password=UUID)
- `Multiplex`, `MuxPadding`, `MuxMaxStreams`: sing-box multiplex block (note: `max_streams` is emitted but sing-box rejects it on VLESS inbound — leave `MuxMaxStreams=0`)
- `ExitOutbound`: `""` (=route.final default) | `"direct"` | `"wg-out"` — emits per-inbound `route.rules` entry directing this inbound to a specific outbound
- `RealityPrivateKey`, `RealityPublicKey`, `RealityShortIDs`, `Fingerprint`: per-inbound Reality keys + uTLS fingerprint (`"chrome"` for DPI-resistant handshake)
- `ShadowTLSPassword`, `ShadowTLSVersion`, `CoverDomain`, `InnerMethod`, `InnerPassword`: ShadowTLS-only fields (Protocol="shadowtls")
- `IsBuiltin`: `true` for original seed configs — cannot be deleted via API, only disabled

## Environment Variables

Required: `BOT_TOKEN`, `ADMIN_PASSWORD`, `JWT_SECRET`, `SERVER_IP` (default: 49.13.201.110), `ADMIN_ID` (Telegram ID).

### Config generation (etap 1 rework)

- `EXTRA_OUTBOUND_JSON_PATH` — optional path to a JSON file containing a single outbound object. If set and readable, its contents are prepended to `outbounds[]` at config generation time. **On RuVDS:** `/etc/vpnbot/extra-outbound.json` contains the `wg-out` block. On Hetzner: unset.
- `ROUTE_FINAL` — overrides `route.final` tag (default `"direct"`). **On RuVDS:** `wg-out`. On Hetzner: unset.

Note: these are relevant only if `vpnbot` binary runs on that host. Currently `vpnbot` runs only on Hetzner (see topology below).

### Network Management (optional)

- `HETZNER_API_TOKEN` — Hetzner Cloud API token for firewall management (`service/firewall.go`)
- `HETZNER_SERVER_IP` — Hetzner server IP (default: falls back to `SERVER_IP` → 49.13.201.110)
- `RUVDS_IP` — RuVDS public IP (194.87.80.237)
- `RUVDS_SSH_USER` — SSH user (default: root)
- `RUVDS_SSH_KEY_PATH` — Path to SSH private key (default: ~/.ssh/id_rsa)
- `RUVDS_SSH_PORT` — SSH port (default: 22)

## Production topology (2026-07-11)

Two servers, complementary roles:

- **RuVDS** (194.87.80.237, RU): Frontend — clients connect here. Runs sing-box (**hand-managed config**, `vpnbot` binary NOT deployed here). Terminates VLESS Reality + ShadowTLS handshakes.
- **Hetzner** (49.13.201.110, DE): Backend — runs `vpnbot`, admin API (Caddy TLS on `myvpn-api.online:8443` → `:8085`), Telegram bot. Also runs sing-box as fallback / exit for the WireGuard tunnel from RuVDS.

### Traffic paths

```
[DE-*]  Client ─VLESS Reality─▶ RuVDS ─wg0 (UDP :51820)─▶ Hetzner ─direct─▶ Internet
                               │
[RU]    Client ─VLESS Reality─▶ RuVDS ─direct (via routing_mark=100)─▶ Internet
                               │  ↑
                               │  └─ nftables NFQUEUE 100 ─▶ nfqws --dpi-desync=split (etap 3)
                               │
[RU-STLS] Client ─ShadowTLS v3─▶ RuVDS:8446 ─inner shadowsocks (loopback)─▶ Internet
```

- `[DE-*]` inbounds have `ExitOutbound=""` (default) — traffic terminates on RuVDS then egresses via WireGuard wg-tunnel to Hetzner (`route.final=wg-out`).
- `[RU]`/`[RU-TCP]` inbounds have `ExitOutbound="direct"` — traffic egresses from RuVDS `eth0` with Russian IP. `direct` outbound has `routing_mark=100`; nftables `table inet zapret` matches marked TCP :80/443/8080/8443 and feeds it to `nfqws` for DPI desynchronization.
- `[RU-STLS]` is ShadowTLS v3 wrapping inner shadowsocks — resistant to Reality-detection heuristics.

**No iptables DNAT/MASQUERADE on RuVDS.** The legacy port-forwarder in `service/portforward.go` is dormant — actual routing goes through sing-box + WireGuard.

### Config divergence between servers

The single `InboundConfig` DB table on Hetzner drives both servers' inbound sets, but only Hetzner's `vpnbot` auto-regenerates `/etc/sing-box/config.json`. Changes affecting RuVDS require **manual config updates** on RuVDS (typically small python scripts pushed via `scp` + `sing-box check` + `systemctl reload sing-box`). See `deploy/ruvds/` for artifacts.

## Testing

Unit tests in `service/vpn_test.go` cover config generation (routing, multiplex, shadowtls-group) and subscription link generation. Run with `go test ./service/ -v`.

Manual verification remains critical for changes affecting sing-box behavior — after `POST /api/reload`, `journalctl -u sing-box -n 20` and `sing-box check -c /etc/sing-box/config.json` should be inspected. The `POST /api/reload` returns success even if sing-box rejects the config (SIGHUP is async).

## API Structure

- Public: `GET /sub/:token`
- Auth: `POST /api/login` → JWT
- Protected: `/api/users/*`, `/api/inbounds/*`, `/api/inbounds/validate-sni`, `/api/stats`, `POST /api/reload`
- Network: `/api/network/status`, `/api/network/firewall/*`, `/api/network/forwards/*`, `/api/network/ping`, `/api/network/check-all`

Server listens on `:8085` (proxied via Caddy on `myvpn-api.online:8443`).

## Language

Code comments and user-facing bot messages are in Russian. API responses and technical identifiers are in English.
