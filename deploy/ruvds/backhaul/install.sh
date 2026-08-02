#!/usr/bin/env bash
# Установка фронтенд-стороны тройного backhaul на RuVDS.
#
# ВАЖНО ПРО ПОРЯДОК: этот скрипт НЕ трогает боевые порты и НЕ снимает старый
# DNAT. Relay поднимается на смещённых (staging) портах, чтобы прогнать полный
# end-to-end параллельно работающему проду. Перевод боевых портов — отдельный
# шаг, promote.sh, с автоматическим откатом.
#
# Ставит:
#   1. frps            — точка входа для израильского узла (он за CGNAT);
#   2. sing-box-bh2    — адаптер secondary: SOCKS5 → vless+ws → Yandex Cloud → Hetzner;
#   3. SSH-туннель emergency: RuVDS → Hetzner (`ssh -D`), SOCKS5 на loopback;
#   4. sing-box-relay  — сам L4-relay (staging-порты);
#   5. backhaul-monitor — health checker с гистерезисом;
#   6. nftables        — default deny, ничего лишнего наружу.
#
# Запуск: на RuVDS, от root.
#   ./install.sh /path/to/params.env
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

STAMP="$(date -u +%Y%m%d-%H%M%S)"
BACKUP_DIR="/root/backhaul-backups/${STAMP}"
mkdir -p "$BACKUP_DIR" /etc/backhaul /var/lib/vpnbot

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

backup() {
  local f="$1"
  [[ -e "$f" ]] || return 0
  install -D -m 0600 "$f" "${BACKUP_DIR}${f}"
  log "бэкап: $f"
}

require() {
  local v="$1"
  [[ -n "${!v:-}" ]] || { echo "параметр $v не задан в $PARAMS" >&2; exit 1; }
}

require BACKEND_HOST
require FRP_TOKEN
require FRP_VERSION
require FRP_BIND_PORT
require CLASH_API_SECRET
require BHWS_UUID
require YC_DOMAIN
require STAGING_PORT_OFFSET

# Полный снимок текущего сетевого состояния — до любых изменений.
log "снимок текущего состояния сети → ${BACKUP_DIR}"
{
  echo "### iptables-save"; iptables-save 2>/dev/null || true
  echo "### ip6tables-save"; ip6tables-save 2>/dev/null || true
  echo "### nft list ruleset"; nft list ruleset 2>/dev/null || true
  echo "### ss -tlpn"; ss -tlpn 2>/dev/null || true
  echo "### systemctl running"; systemctl list-units --type=service --state=running --no-pager --no-legend 2>/dev/null || true
} > "${BACKUP_DIR}/network-snapshot.txt"
backup /etc/nftables.conf

# ───────────────────────────── 1. frps ─────────────────────────────
log "frps ${FRP_VERSION} — вход для израильского узла на :${FRP_BIND_PORT}"
if [[ ! -x /usr/local/bin/frps ]] || ! /usr/local/bin/frps -v 2>/dev/null | grep -q "${FRP_VERSION}"; then
  tmp="$(mktemp -d)"
  curl -fsSL --retry 3 -o "$tmp/frp.tgz" \
    "https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/frp_${FRP_VERSION}_linux_amd64.tar.gz"
  tar -xzf "$tmp/frp.tgz" -C "$tmp"
  install -m 0755 "$tmp/frp_${FRP_VERSION}_linux_amd64/frps" /usr/local/bin/frps
  rm -rf "$tmp"
fi

backup /etc/backhaul/frps.toml
cat > /etc/backhaul/frps.toml <<EOF
# frps: принимает reverse-соединение израильского узла.
bindAddr = "0.0.0.0"
bindPort = ${FRP_BIND_PORT}

auth.method = "token"
auth.token = "${FRP_TOKEN}"

# TLS обязателен: plaintext-клиентов не принимаем вовсе.
transport.tls.force = true

# КЛЮЧЕВОЕ: мультиплексирование выключено. Иначе весь трафик всех
# пользователей и обоих сервисов схлопнулся бы в один TCP-поток —
# ровно то, чего делать нельзя.
transport.tcpMux = false

# Опубликованные проксями порты слушают ТОЛЬКО на loopback: SOCKS5 наружу
# не выставляется ни при каком раскладе.
proxyBindAddr = "127.0.0.1"

# Клиенту разрешены ровно два порта — наши адаптеры, и ничего больше.
allowPorts = [
  { single = ${FRP_SOCKS_VLESS_PORT} },
  { single = ${FRP_SOCKS_MTPROTO_PORT} }
]

transport.heartbeatTimeout = 60
transport.maxPoolCount = 0

log.to = "console"
log.level = "info"
EOF
chmod 600 /etc/backhaul/frps.toml

backup /etc/systemd/system/frps.service
cat > /etc/systemd/system/frps.service <<'EOF'
[Unit]
Description=frps — точка входа резидентного израильского узла (backhaul primary)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/frps -c /etc/backhaul/frps.toml
Restart=always
RestartSec=5
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
LoadCredential=config:/etc/backhaul/frps.toml

[Install]
WantedBy=multi-user.target
EOF

# ─────────────────── 2. sing-box-bh2 (secondary adapter) ───────────────────
log "sing-box-bh2 — адаптер secondary (SOCKS5 → vless+ws → ${YC_DOMAIN})"
SB_BIN="${SINGBOX_RELAY_BIN:-/usr/local/bin/sing-box}"
[[ -x "$SB_BIN" ]] || { echo "нет sing-box по пути $SB_BIN" >&2; exit 1; }
SB_VER="$("$SB_BIN" version | head -1)"
log "используем уже установленный ${SB_VER}"

mkdir -p /etc/sing-box-bh2
backup /etc/sing-box-bh2/config.json
cat > /etc/sing-box-bh2/config.json <<EOF
{
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [
    {
      "type": "socks",
      "tag": "bh2-in-vless",
      "listen": "127.0.0.1",
      "listen_port": ${SECONDARY_SOCKS_VLESS_PORT}
    },
    {
      "type": "socks",
      "tag": "bh2-in-mtproto",
      "listen": "127.0.0.1",
      "listen_port": ${SECONDARY_SOCKS_MTPROTO_PORT}
    }
  ],
  "outbounds": [
    {
      "type": "vless",
      "tag": "bh2-out-vless",
      "server": "${YC_DOMAIN}",
      "server_port": 443,
      "uuid": "${BHWS_UUID}",
      "tls": { "enabled": true, "server_name": "${YC_DOMAIN}" },
      "transport": { "type": "ws", "path": "${BHWS_PATH}" }
    },
    {
      "type": "vless",
      "tag": "bh2-out-mtproto",
      "server": "${YC_DOMAIN}",
      "server_port": 443,
      "uuid": "${BHWS_UUID}",
      "tls": { "enabled": true, "server_name": "${YC_DOMAIN}" },
      "transport": { "type": "ws", "path": "${BHWS_PATH}" }
    }
  ],
  "route": {
    "rules": [
      { "inbound": ["bh2-in-vless"],   "outbound": "bh2-out-vless" },
      { "inbound": ["bh2-in-mtproto"], "outbound": "bh2-out-mtproto" },
      { "action": "reject" }
    ]
  }
}
EOF
chmod 600 /etc/sing-box-bh2/config.json
"$SB_BIN" check -c /etc/sing-box-bh2/config.json

backup /etc/systemd/system/sing-box-bh2.service
cat > /etc/systemd/system/sing-box-bh2.service <<EOF
[Unit]
Description=sing-box: адаптер backhaul secondary (Yandex Cloud → Hetzner)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${SB_BIN} run -c /etc/sing-box-bh2/config.json
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=5
LimitNOFILE=65536
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
StateDirectory=sing-box-bh2
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
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

# ─────────────────── 3. SSH-туннель emergency (инициирует RuVDS) ───────────────────
# Направление инвертировано относительно изначального замысла: см. params.env.
# Коротко — RuVDS не принимает входящий TCP извне РФ, поэтому `ssh -R` с
# Hetzner установиться не может. Инициирует RuVDS, результат тот же:
# SOCKS5 на 127.0.0.1 здесь, выход на Hetzner.
log "SSH-туннель к Hetzner (emergency)"
if [[ ! -f "${SSH_KEY_PATH}" ]]; then
  ssh-keygen -t ed25519 -N '' -C 'backhaul-emergency-ruvds' -f "${SSH_KEY_PATH}" >/dev/null
  log "сгенерирован ключ ${SSH_KEY_PATH}"
fi
chmod 600 "${SSH_KEY_PATH}"

# Строгая проверка ключа хоста должна быть настоящей, а не декоративной.
if [[ ! -s /etc/backhaul/known_hosts ]]; then
  if ssh-keyscan -T 10 -p "${SSH_PORT:-22}" "${HETZNER_IP}" > /etc/backhaul/known_hosts 2>/dev/null \
     && [[ -s /etc/backhaul/known_hosts ]]; then
    log "ключ хоста Hetzner записан"
  else
    warn "не удалось получить ключ хоста Hetzner — emergency не поднимется"
  fi
fi
chmod 600 /etc/backhaul/known_hosts 2>/dev/null || true

backup /etc/systemd/system/backhaul-fssh@.service
cat > /etc/systemd/system/backhaul-fssh@.service <<EOF
[Unit]
Description=backhaul emergency: SSH-туннель RuVDS → Hetzner для класса %i
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
EnvironmentFile=/etc/backhaul/fssh-%i.env
# -D <порт> = dynamic forwarding: SOCKS5 слушает здесь, на 127.0.0.1,
# а соединения открываются на стороне Hetzner. Адресаты ограничены
# директивой PermitOpen в sshd Hetzner — туннель не может открыть ничего,
# кроме backend-портов.
ExecStart=/usr/bin/ssh -NT \\
  -o ExitOnForwardFailure=yes \\
  -o ServerAliveInterval=15 \\
  -o ServerAliveCountMax=3 \\
  -o TCPKeepAlive=yes \\
  -o StrictHostKeyChecking=yes \\
  -o UserKnownHostsFile=/etc/backhaul/known_hosts \\
  -o IdentitiesOnly=yes \\
  -o BatchMode=yes \\
  -o ConnectTimeout=10 \\
  -o ForwardAgent=no \\
  -o ForwardX11=no \\
  -o RequestTTY=no \\
  -o ControlMaster=no \\
  -o ControlPath=none \\
  -i ${SSH_KEY_PATH} \\
  -p ${SSH_PORT:-22} \\
  -D 127.0.0.1:\${SOCKS_PORT} \\
  ${SSH_USER}@${HETZNER_IP}
Restart=always
RestartSec=10
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadOnlyPaths=/etc/backhaul
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
SystemCallArchitectures=native
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/backhaul/fssh-vless.env <<EOF
SOCKS_PORT=${EMERGENCY_SOCKS_VLESS_PORT}
EOF
cat > /etc/backhaul/fssh-mtproto.env <<EOF
SOCKS_PORT=${EMERGENCY_SOCKS_MTPROTO_PORT}
EOF
chmod 600 /etc/backhaul/fssh-*.env

log "публичный ключ для Hetzner (RUVDS_TUNNEL_PUBKEY в params.env):"
cat "${SSH_KEY_PATH}.pub"

# ───────────────────── 4. relay + конфиг backhaul ─────────────────────
log "backhaul.json + sing-box-relay (staging-порты, смещение ${STAGING_PORT_OFFSET})"
# Бинарь приезжает рядом со скриптом (его кладёт push.sh с Hetzner).
# /opt/VpnBot/bin — запасной путь на случай ручной раскладки.
BH_BIN=""
for cand in "$(dirname "$0")/backhaul-monitor" /opt/VpnBot/bin/backhaul-monitor; do
  [[ -x "$cand" ]] && { BH_BIN="$cand"; break; }
done
[[ -n "$BH_BIN" ]] || { echo "не найден бинарь backhaul-monitor — запустите deploy/ruvds/backhaul/push.sh с Hetzner" >&2; exit 1; }
install -D -m 0755 "$BH_BIN" /usr/local/bin/backhaul-monitor

backup /etc/vpnbot/backhaul.json
backup "${SINGBOX_RELAY_DIR}/config.json"
mkdir -p /etc/vpnbot "${SINGBOX_RELAY_DIR}" /var/lib/sing-box-relay
if [[ "${PRIMARY_ENABLED:-false}" != "true" ]]; then
  warn "primary выключен (PRIMARY_ENABLED=false): активны secondary и emergency"
fi
# Единый генератор для staging и прода — чтобы режимы не разъехались.
MODE="${MODE:-staging}" "$(dirname "$0")/render-config.sh" "$PARAMS"

backup /etc/systemd/system/sing-box-relay.service
cat > /etc/systemd/system/sing-box-relay.service <<EOF
[Unit]
Description=sing-box: L4-relay VLESS/MTProto с тремя backhaul'ами
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${SB_BIN} run -c ${SINGBOX_RELAY_DIR}/config.json
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/sing-box-relay
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
# Нужна только привязка к привилегированным портам (443 и т.п.).
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

# ───────────────────────── 5. монитор ─────────────────────────
log "backhaul-monitor"
backup /etc/systemd/system/backhaul-monitor.service
cat > /etc/systemd/system/backhaul-monitor.service <<'EOF'
[Unit]
Description=backhaul health checker (failover VLESS/MTProto с гистерезисом)
After=network-online.target sing-box-relay.service
Wants=network-online.target
Requires=sing-box-relay.service

[Service]
Type=simple
ExecStart=/usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json
Restart=always
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/vpnbot /var/log
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
RestrictAddressFamilies=AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

# ───────────────────────── запуск ─────────────────────────
systemctl daemon-reload
if [[ "${PRIMARY_ENABLED:-false}" == "true" ]]; then
  systemctl enable --now frps.service
else
  # Порт наружу не открываем и демон не поднимаем, пока подключаться некому.
  systemctl disable --now frps.service >/dev/null 2>&1 || true
  log "frps установлен, но не запущен — израильского узла пока нет"
fi
if [[ "${SECONDARY_ENABLED:-false}" == "true" ]]; then
  systemctl enable --now sing-box-bh2.service
else
  # Без ВМ в Yandex Cloud адаптеру некуда подключаться: он будет циклиться
  # на резолве домена и мусорить в журнале. Конфиг записан и проверен —
  # включить можно одной командой, когда ВМ появится.
  systemctl disable --now sing-box-bh2.service >/dev/null 2>&1 || true
  log "sing-box-bh2 настроен, но не запущен (SECONDARY_ENABLED=false — нет ВМ в Yandex Cloud)"
fi
systemctl enable --now sing-box-relay.service
systemctl enable --now backhaul-fssh@vless.service backhaul-fssh@mtproto.service || \
  warn "SSH-туннель не поднялся — проверьте authorized_keys пользователя ${SSH_USER} на Hetzner"
systemctl enable --now backhaul-monitor.service

log "готово. Бэкапы: ${BACKUP_DIR}"
log "дальше: 1) ./nftables-apply.sh  2) scripts/backhaul/verify.sh  3) ./promote.sh"
