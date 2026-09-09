# VpnBot

Control plane for a small censorship-circumvention service aimed at users behind
DPI-based blocking (Russia's TSPU). A Go backend, a Telegram bot and a Next.js
admin panel generate [sing-box](https://sing-box.sagernet.org/) configuration,
manage users and subscription links, and operate a two-node relay topology:
a domestically hosted front node that terminates client connections, and a
foreign exit node reached over a WireGuard backhaul.

This is defensive, anti-censorship infrastructure. There is no offensive
tooling here.

## What it does

- **Transports** — VLESS + Reality (TCP, HTTP/2, gRPC, XHTTP), Hysteria2,
  ShadowTLS v3 with an inner Shadowsocks, AnyTLS; per-inbound Reality keys,
  uTLS fingerprints and multiplex settings live in the database and are
  rendered into sing-box JSON by a pure builder (`service/vpn.go`).
- **Relay topology** — the front node's sing-box is configured remotely over
  SSH; traffic egresses either locally (with `nfqws` DPI desynchronisation via
  nftables) or through a WireGuard tunnel to the exit node
  (`service/singboxruvds.go`, `service/wireguard.go`, `deploy/ruvds/zapret/`).
- **Backhaul failover** — `service/backhaul/` probes several paths between the
  two nodes (direct WireGuard, WSS via a CDN, emergency SSH), decides which one
  is healthy and switches with a rollback drill (`scripts/backhaul/`).
- **Fallbacks** — MTProto proxy via `telemt` with FakeTLS, and a TURN-based
  tunnel bootstrapped from a public VoIP service (`service/telemt*.go`,
  `service/turnproxy.go`).
- **Operations** — health monitoring with Telegram alerts, Hetzner Cloud
  firewall management, nftables port forwarding, a decoy website for the
  front node, idempotent install scripts under `deploy/`.
- **Users** — Telegram bot with approval flow and QR codes; REST API and admin
  panel for users, inbounds, stats and network status; base64 subscription
  endpoints for VPN clients.

## Architecture

```
Client ──VLESS Reality / ShadowTLS / Hysteria2──▶ front node (RU)
                                                    │
                                    ┌───────────────┴───────────────┐
                                    ▼                               ▼
                       direct egress + nfqws               WireGuard backhaul
                       (DPI desync, RU exit)                        │
                                                                    ▼
                                                          exit node (EU) ──▶ Internet
                                                          runs vpnbot, API, bot
```

Request flow inside the backend: `gin router → JWT middleware → handlers →
GORM/SQLite + sing-box config generation`.

## Repository layout

| Path | Contents |
| --- | --- |
| `api/` | REST handlers, JWT middleware, routes |
| `bot/` | Telegram bot and health alarms |
| `database/` | GORM models, migrations, first-run seeding |
| `service/` | sing-box config builder, link generation, WireGuard, firewall, port forwarding, telemt, TURN, backhaul, health |
| `cmd/` | `backhaul-probe` and `backhaul-monitor` binaries |
| `deploy/` | Install scripts for the exit node, the front node, an IL node and a Yandex Cloud CDN leg; nftables/nfqws rules; decoy site |
| `docs/` | Design specs and implementation plans for each iteration (DPI hardening, MTProxy variants, multipath slipstream client, AnyTLS relay), plus runbooks |
| `vpn-admin-panel/` | Next.js admin panel |

`docs/superpowers/specs` is the best place to start if you want to understand
*why* the system looks the way it does.

## Build

```sh
go build -o vpnbot .        # backend
go vet ./... && go test ./...
cd vpn-admin-panel && npm install && npm run build
```

CI runs `go vet` and `go build` on pull requests; pushes to `main` deploy to
the exit node over SSH.

## Configuration

Everything environment-specific comes from `.env` (see `.env.example`) or from
GitHub Actions secrets used by `.github/workflows/deploy.yml`. Nothing sensitive
is committed:

- server addresses and domains are never hard-coded — `SERVER_IP` is required
  at start-up;
- Reality keypairs, short IDs and user UUIDs are generated on first run and
  stored only in the local SQLite database;
- WireGuard/SSH keys and backhaul secrets are git-ignored.

Placeholders such as `<HETZNER_IP>` or `<RUVDS_IP>` in the docs stand for real
addresses that are intentionally not published.

## Language

Code comments, bot messages and most design docs are in Russian; identifiers,
API responses and this README are in English.
