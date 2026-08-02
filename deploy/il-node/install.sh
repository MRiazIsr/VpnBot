#!/usr/bin/env bash
# Установка резидентного узла (Израиль) — backhaul primary.
#
# Узел стоит за NAT/CGNAT, входящих соединений не принимает: он сам идёт
# наружу к frps на RuVDS (TLS + token), и через это соединение RuVDS получает
# у себя на 127.0.0.1 два SOCKS5-адаптера. Дальше узел выпускает трафик к
# backend'у Hetzner по WireGuard.
#
#   Телефон → RuVDS:порт → relay → SOCKS5 127.0.0.1 → frps
#           → (reverse, TLS) → этот узел → wg1 → Hetzner:тот же порт
#
# В домашнюю сеть узла ничего не заворачивается: через туннель ходит только
# 10.9.0.0/24, весь остальной трафик машины идёт как обычно.
#
# Запуск на самом узле, от root:
#   ./install.sh /path/to/params.env /path/to/wg1_il.key
set -euo pipefail

PARAMS="${1:?укажите params.env}"
IL_WG_KEY="${2:?укажите файл приватного ключа wg (получен с Hetzner: /etc/backhaul/wg1_il.key)}"
# shellcheck disable=SC1090
source "$PARAMS"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

for v in RUVDS_IP FRP_BIND_PORT FRP_TOKEN FRP_VERSION FRP_SOCKS_VLESS_PORT \
         FRP_SOCKS_MTPROTO_PORT HETZNER_IP WG_PORT WG_SUBNET IL_WG_ADDRESS \
         HETZNER_WG_PUBKEY; do
  [[ -n "${!v:-}" ]] || { echo "параметр $v не задан" >&2; exit 1; }
done

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  FRP_ARCH=amd64 ;;
  aarch64) FRP_ARCH=arm64 ;;
  armv7l)  FRP_ARCH=arm ;;
  *) echo "неподдерживаемая архитектура $ARCH" >&2; exit 1 ;;
esac

mkdir -p /etc/backhaul
chmod 700 /etc/backhaul

# ───────────────────────────── frpc ─────────────────────────────
log "frpc ${FRP_VERSION} (${FRP_ARCH})"
if [[ ! -x /usr/local/bin/frpc ]] || ! /usr/local/bin/frpc -v 2>/dev/null | grep -q "${FRP_VERSION}"; then
  tmp="$(mktemp -d)"
  curl -fsSL --retry 3 -o "$tmp/frp.tgz" \
    "https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/frp_${FRP_VERSION}_linux_${FRP_ARCH}.tar.gz"
  tar -xzf "$tmp/frp.tgz" -C "$tmp"
  install -m 0755 "$tmp/frp_${FRP_VERSION}_linux_${FRP_ARCH}/frpc" /usr/local/bin/frpc
  rm -rf "$tmp"
fi

cat > /etc/backhaul/frpc.toml <<EOF
# Резидентный узел (Израиль) → frps на RuVDS.
serverAddr = "${RUVDS_IP}"
serverPort = ${FRP_BIND_PORT}

auth.method = "token"
auth.token = "${FRP_TOKEN}"

transport.tls.enable = true

# Мультиплексирование выключено намеренно: иначе трафик всех пользователей и
# обоих сервисов схлопнулся бы в один TCP-поток.
transport.tcpMux = false

# Переподключение и keepalive: узел домашний, разрывы — норма жизни.
transport.dialServerTimeout = 10
transport.dialServerKeepalive = 7200
transport.heartbeatInterval = 20
transport.heartbeatTimeout = 60
loginFailExit = false

log.to = "console"
log.level = "info"

# Два отдельных адаптера: VLESS и MTProto не делят ни поток, ни порт.
[[proxies]]
name = "socks-vless"
type = "tcp"
remotePort = ${FRP_SOCKS_VLESS_PORT}
[proxies.plugin]
type = "socks5"

[[proxies]]
name = "socks-mtproto"
type = "tcp"
remotePort = ${FRP_SOCKS_MTPROTO_PORT}
[proxies.plugin]
type = "socks5"
EOF
chmod 600 /etc/backhaul/frpc.toml

# ───────────────────────── WireGuard → Hetzner ─────────────────────────
log "WireGuard ${WG_IF:-wg1} → Hetzner"
apt-get install -y wireguard-tools >/dev/null 2>&1 || \
  warn "поставьте wireguard-tools вручную"
install -m 600 "$IL_WG_KEY" /etc/backhaul/wg_il.key
mkdir -p /etc/wireguard
chmod 700 /etc/wireguard

cat > "/etc/wireguard/${WG_IF:-wg1}.conf" <<EOF
# Резидентный узел → backend Hetzner. Managed by deploy/il-node/install.sh.
[Interface]
Address = ${IL_WG_ADDRESS}/24
PrivateKey = $(cat /etc/backhaul/wg_il.key)

[Peer]
PublicKey = ${HETZNER_WG_PUBKEY}
Endpoint = ${HETZNER_IP}:${WG_PORT}
# ТОЛЬКО подсеть backhaul. Домашний трафик машины через туннель не идёт.
AllowedIPs = ${WG_SUBNET}
PersistentKeepalive = 25
EOF
chmod 600 "/etc/wireguard/${WG_IF:-wg1}.conf"

systemctl enable --now "wg-quick@${WG_IF:-wg1}"

# ───────────────────────────── systemd ─────────────────────────────
cat > /etc/systemd/system/frpc-backhaul.service <<EOF
[Unit]
Description=frpc — резидентный backhaul primary (→ RuVDS)
After=network-online.target wg-quick@${WG_IF:-wg1}.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/frpc -c /etc/backhaul/frpc.toml
Restart=always
RestartSec=10
LimitNOFILE=65536
DynamicUser=yes
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
RestrictAddressFamilies=AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=
LoadCredential=config:/etc/backhaul/frpc.toml

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now frpc-backhaul.service

log "проверка: доступен ли backend Hetzner через туннель"
if ping -c 2 -W 3 "${BACKEND_HOST}" >/dev/null 2>&1; then
  log "${BACKEND_HOST} отвечает — WireGuard поднят"
else
  warn "${BACKEND_HOST} не отвечает: проверьте ufw на Hetzner (allow in on ${WG_IF:-wg1}) и ключи"
fi

log "готово. На RuVDS должны появиться 127.0.0.1:${FRP_SOCKS_VLESS_PORT} и :${FRP_SOCKS_MTPROTO_PORT}"
log "после этого на RuVDS: PRIMARY_ENABLED=true в params.env → ./render-config.sh → systemctl restart sing-box-relay backhaul-monitor"
