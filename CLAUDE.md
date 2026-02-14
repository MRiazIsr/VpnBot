# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run Commands

```bash
# Backend (Go)
go build -o vpnbot .          # Build binary
go vet ./...                   # Lint/vet
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
- **`service/`** — sing-box JSON config generation (`GenerateAndReload()`), subscription link generation (`GenerateLinkForInbound()`), traffic tracking via gRPC V2Ray Stats API
- **`api/handlers/`** — REST handlers: auth, users, inbounds CRUD, stats, public subscription endpoints
- **`api/middleware/`** — CORS and JWT Bearer auth
- **`api/router/`** — Route registration under `/api` with auth group
- **`bot/`** — Telegram bot (telebot.v3) with dynamic connection buttons from DB, QR codes

### Key data flow

1. `InboundConfig` records in DB define VPN inbounds (vless/hysteria2, ports, TLS, transport, Reality keys)
2. `GenerateAndReload()` reads enabled `InboundConfig` + active `User` from DB → builds sing-box JSON → writes `/etc/sing-box/config.json` → `systemctl reload sing-box`
3. Subscription endpoint (`/sub/:token`) calls `GenerateLinkForInbound()` for each enabled inbound → returns base64-encoded links
4. Bot dynamically generates connection buttons from enabled InboundConfig records in DB

### InboundConfig model

Drives both sing-box config generation and subscription links. Key fields:
- `Protocol`: "vless" | "hysteria2"
- `TLSType`: "reality" (uses per-inbound Reality keys) | "certificate" (uses cert_path/key_path)
- `UserType`: "legacy" (with flow) | "new" (no flow) | "hy2" (password=UUID)
- `Transport`: "" (TCP) | "http" | "grpc"
- `ListenPort`: port for this inbound (required)
- `SNI`: server name for TLS/Reality handshake (required for Reality inbounds)
- `RealityPrivateKey`, `RealityPublicKey`, `RealityShortIDs`: per-inbound Reality keys
- `Fingerprint`: TLS fingerprint for links (default "random")
- `IsBuiltin`: true for 4 seed configs — cannot be deleted via API, only disabled

## Environment Variables

`BOT_TOKEN`, `ADMIN_PASSWORD`, `JWT_SECRET`, `SERVER_IP` (default: 49.13.201.110), `ADMIN_ID` (Telegram ID).

## API Structure

- Public: `GET /sub/:token`
- Auth: `POST /api/login` → JWT
- Protected: `/api/users/*`, `/api/inbounds/*`, `/api/inbounds/validate-sni`, `/api/stats`, `POST /api/reload`

Server listens on `:8085` (proxied via nginx).

## Language

Code comments and user-facing bot messages are in Russian. API responses and technical identifiers are in English.
