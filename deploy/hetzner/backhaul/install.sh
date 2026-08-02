#!/usr/bin/env bash
# Установка backend-стороны тройного backhaul на Hetzner.
#
# Что делает (всё идемпотентно, всё с бэкапами):
#   1. wg1 — отдельный WireGuard-интерфейс для израильского узла;
#   2. backhaul-probe — эндпоинт измерения, слушает ТОЛЬКО на адресе wg1;
#   3. sing-box-bhws — терминатор WSS-плеча (vless+ws на loopback, за Caddy);
#   4. Caddy — путь ${BHWS_PATH} на существующем домене → sing-box-bhws;
#   5. sshd — приём SSH-туннеля с RuVDS (emergency), PermitOpen строго на backend;
#   6. ufw — доступ к backend-портам изнутри wg1.
#
# НИЧЕГО из существующего не выключает: старые публичные порты, старый wg0,
# telemt и основной sing-box остаются как есть.
#
# Запуск: на Hetzner, от root.
#   ./install.sh /path/to/params.env
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

STAMP="$(date -u +%Y%m%d-%H%M%S)"
BACKUP_DIR="/root/backhaul-backups/${STAMP}"
mkdir -p "$BACKUP_DIR" /etc/backhaul

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

backup() {
  local f="$1"
  [[ -e "$f" ]] || return 0
  install -D -m 0600 "$f" "${BACKUP_DIR}${f}"
  log "бэкап: $f → ${BACKUP_DIR}${f}"
}

require() {
  local v="$1"
  [[ -n "${!v:-}" ]] || { echo "параметр $v не задан в $PARAMS" >&2; exit 1; }
}

require BACKEND_HOST
require WG_IF
require WG_PORT
require WG_SUBNET
require RUVDS_IP
require BHWS_UUID
require BHWS_PATH
require BHWS_BACKEND_PORT
require PROBE_LISTEN

# ───────────────────────────── 1. wg1 ─────────────────────────────
log "WireGuard ${WG_IF} (IL residential ↔ Hetzner)"
WG_CONF="/etc/wireguard/${WG_IF}.conf"
mkdir -p /etc/wireguard
chmod 700 /etc/wireguard

if [[ ! -f /etc/backhaul/wg1_hetzner.key ]]; then
  umask 077
  wg genkey > /etc/backhaul/wg1_hetzner.key
  wg pubkey < /etc/backhaul/wg1_hetzner.key > /etc/backhaul/wg1_hetzner.pub
  log "сгенерирован ключ Hetzner для ${WG_IF}"
fi
if [[ ! -f /etc/backhaul/wg1_il.key ]]; then
  umask 077
  wg genkey > /etc/backhaul/wg1_il.key
  wg pubkey < /etc/backhaul/wg1_il.key > /etc/backhaul/wg1_il.pub
  log "сгенерирован ключ израильского узла (отдать на узел вместе с install.sh)"
fi

HETZ_PRIV="$(cat /etc/backhaul/wg1_hetzner.key)"
IL_PUB="$(cat /etc/backhaul/wg1_il.pub)"

backup "$WG_CONF"
cat > "$WG_CONF" <<EOF
# Managed by deploy/hetzner/backhaul/install.sh — не редактировать руками.
# Отдельный интерфейс: wg0 принадлежит старому туннелю RuVDS↔Hetzner и
# перегенерируется кодом vpnbot.
[Interface]
Address = ${BACKEND_HOST}/24
ListenPort = ${WG_PORT}
PrivateKey = ${HETZ_PRIV}

[Peer]
# Израильский резидентный узел (за NAT/CGNAT — инициирует всегда он)
PublicKey = ${IL_PUB}
AllowedIPs = ${IL_WG_ADDRESS:-10.9.0.2}/32
PersistentKeepalive = 25
EOF
chmod 600 "$WG_CONF"

systemctl enable --now "wg-quick@${WG_IF}" >/dev/null 2>&1 || systemctl restart "wg-quick@${WG_IF}"
ip -br addr show "$WG_IF" || warn "интерфейс ${WG_IF} не поднялся"

# ufw: пускаем внутрь wg1. Без этого default deny рубит backend-порты —
# ровно эта ошибка, судя по нулевому handshake, и убила старый wg0.
log "ufw: разрешаем вход с ${WG_SUBNET} на ${WG_IF}"
ufw allow in on "${WG_IF}" comment 'backhaul: IL residential leg' >/dev/null
ufw allow "${WG_PORT}/udp" comment 'backhaul: wg1 (IL)' >/dev/null

# ───────────────────────────── 2. probe ─────────────────────────────
log "backhaul-probe → ${PROBE_LISTEN}"
install -D -m 0755 /opt/VpnBot/bin/backhaul-probe /usr/local/bin/backhaul-probe

backup /etc/systemd/system/backhaul-probe.service
cat > /etc/systemd/system/backhaul-probe.service <<EOF
[Unit]
Description=backhaul probe endpoint (измерение backhaul'ов из RuVDS)
After=network-online.target wg-quick@${WG_IF}.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/backhaul-probe -listen ${PROBE_LISTEN}
Restart=always
RestartSec=3
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
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
RestrictAddressFamilies=AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

# ───────────────────────── 3. sing-box-bhws ─────────────────────────
# Отдельный инстанс, а НЕ правка /etc/sing-box/config.json: основной конфиг
# перегенерируется vpnbot'ом и затёр бы наши правки при первом же reload.
log "sing-box-bhws — терминатор WSS-плеча на 127.0.0.1:${BHWS_BACKEND_PORT}"
mkdir -p /etc/sing-box-bhws
backup /etc/sing-box-bhws/config.json
cat > /etc/sing-box-bhws/config.json <<EOF
{
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [
    {
      "type": "vless",
      "tag": "bhws-in",
      "listen": "127.0.0.1",
      "listen_port": ${BHWS_BACKEND_PORT},
      "users": [ { "uuid": "${BHWS_UUID}" } ],
      "transport": { "type": "ws", "path": "${BHWS_PATH}" }
    }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "route": { "rules": [], "final": "direct" }
}
EOF
chmod 600 /etc/sing-box-bhws/config.json

SB_BIN="$(command -v sing-box || echo /usr/local/bin/sing-box)"
"$SB_BIN" check -c /etc/sing-box-bhws/config.json

backup /etc/systemd/system/sing-box-bhws.service
cat > /etc/systemd/system/sing-box-bhws.service <<EOF
[Unit]
Description=sing-box: терминатор WSS-плеча backhaul (secondary)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${SB_BIN} run -c /etc/sing-box-bhws/config.json
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=5
LimitNOFILE=65536
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/sing-box-bhws
StateDirectory=sing-box-bhws
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

# ───────────────────────────── 4. Caddy ─────────────────────────────
# Единственный шаг этого скрипта, который трогает работающий прод. Caddy на
# :8443 отдаёт подписки и админ-API, поэтому здесь: бэкап → правка → validate
# → reload → ПРОВЕРКА, что сайт по-прежнему отвечает → при провале откат.
log "Caddy: ${BHWS_PATH} → 127.0.0.1:${BHWS_BACKEND_PORT}"
CADDYFILE=/etc/caddy/Caddyfile
CADDY_PRE="/tmp/Caddyfile.pre-backhaul.$$"
cp "$CADDYFILE" "$CADDY_PRE"
backup "$CADDYFILE"

# Снимаем эталон: как сайт отвечает ДО наших правок.
caddy_probe() {
  curl -sS --max-time 8 -o /dev/null -w '%{http_code}' \
    "https://${HETZNER_ORIGIN:-myvpn-api.online:8443}/" 2>/dev/null || echo "000"
}
CADDY_BEFORE="$(caddy_probe)"
log "Caddy до правки отвечает: HTTP ${CADDY_BEFORE}"

restore_caddy() {
  warn "откатываем Caddyfile"
  cp "$CADDY_PRE" "$CADDYFILE"
  systemctl reload caddy || systemctl restart caddy
}

if grep -q "handle ${BHWS_PATH}" "$CADDYFILE"; then
  log "маршрут ${BHWS_PATH} уже есть, пропускаем"
else
  # Вставляем handle ПЕРВЫМ блоком внутри существующего сайта myvpn-api.online,
  # чтобы reverse_proxy на 8085 не перехватил WebSocket-апгрейд.
  python3 - "$CADDYFILE" "$BHWS_PATH" "$BHWS_BACKEND_PORT" <<'PY'
import sys, re
path, wspath, port = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path).read()
marker = "myvpn-api.online:8443 {"
i = src.index(marker) + len(marker)
block = f"""
\t# backhaul secondary: WSS-плечо RuVDS → Yandex Cloud → сюда.
\t# Должно стоять выше общего reverse_proxy, иначе апгрейд уйдёт в API.
\thandle {wspath} {{
\t\treverse_proxy 127.0.0.1:{port}
\t}}
"""
open(path, "w").write(src[:i] + block + src[i:])
PY
fi

if ! caddy validate --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1; then
  restore_caddy
  echo "Caddyfile после правки не проходит validate — откатили, ничего не тронуто" >&2
  exit 1
fi
systemctl reload caddy || systemctl restart caddy
sleep 2
CADDY_AFTER="$(caddy_probe)"
if [[ "$CADDY_BEFORE" != "000" && "$CADDY_AFTER" == "000" ]]; then
  restore_caddy
  echo "после перезагрузки Caddy сайт перестал отвечать — откатили" >&2
  exit 1
fi
log "Caddy после правки отвечает: HTTP ${CADDY_AFTER}"
rm -f "$CADDY_PRE"

# ───────────────────── 5. SSH-сервер для emergency ─────────────────────
# Направление инвертировано: инициирует RuVDS, Hetzner принимает.
# Причина в params.env; коротко — RuVDS не принимает входящий TCP извне РФ,
# поэтому `ssh -R` с Hetzner установиться не может в принципе.
log "пользователь ${SSH_USER} для SSH-туннеля с RuVDS"
if ! id -u "${SSH_USER}" >/dev/null 2>&1; then
  useradd --system --create-home --home-dir "/var/lib/${SSH_USER}" \
          --shell /usr/sbin/nologin "${SSH_USER}"
fi
install -d -m 0700 -o "${SSH_USER}" -g "${SSH_USER}" "/var/lib/${SSH_USER}/.ssh"

if [[ -n "${RUVDS_TUNNEL_PUBKEY:-}" ]]; then
  printf '%s\n' "${RUVDS_TUNNEL_PUBKEY}" > "/var/lib/${SSH_USER}/.ssh/authorized_keys"
  chown "${SSH_USER}:${SSH_USER}" "/var/lib/${SSH_USER}/.ssh/authorized_keys"
  chmod 600 "/var/lib/${SSH_USER}/.ssh/authorized_keys"
  log "публичный ключ RuVDS прописан"
else
  warn "RUVDS_TUNNEL_PUBKEY не задан — положите публичный ключ RuVDS в /var/lib/${SSH_USER}/.ssh/authorized_keys"
fi

# Список разрешённых адресатов: ровно наши backend-порты и ничего больше.
# Для dynamic forwarding (-D) PermitOpen проверяет каждый CONNECT, поэтому
# туннель физически не может открыть ничего постороннего — это строже, чем
# было в варианте с `ssh -R`.
PERMIT=""
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
  PERMIT="${PERMIT} ${BACKEND_HOST}:${p}"
done

backup /etc/ssh/sshd_config.d/60-backhaul.conf
mkdir -p /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/60-backhaul.conf <<EOF
# Приём SSH-туннеля с RuVDS. Ограничения только для ${SSH_USER};
# административный доступ root по SSH этим файлом не затрагивается.
Match User ${SSH_USER}
    PubkeyAuthentication yes
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    PermitTTY no
    X11Forwarding no
    AllowAgentForwarding no
    PermitTunnel no
    # Нужен только local/dynamic forward (-D). Обратные форварды не даём.
    AllowTcpForwarding local
    GatewayPorts no
    PermitOpen${PERMIT}
    ForceCommand /usr/sbin/nologin
    ClientAliveInterval 15
    ClientAliveCountMax 3
EOF
sshd -t || { restore_caddy 2>/dev/null || true; echo "sshd_config сломан — правка отменена" >&2; rm -f /etc/ssh/sshd_config.d/60-backhaul.conf; sshd -t; exit 1; }
systemctl reload ssh || systemctl reload sshd
log "sshd принимает туннель от ${SSH_USER}; разрешённые адресаты:${PERMIT}"

# ───────────────────────────── запуск ─────────────────────────────
systemctl daemon-reload
systemctl enable --now backhaul-probe.service
systemctl enable --now sing-box-bhws.service
# Caddy уже перезагружен и проверен выше — второй раз не трогаем.

log "готово. Бэкапы: ${BACKUP_DIR}"
log "публичный ключ wg1 для израильского узла: $(cat /etc/backhaul/wg1_hetzner.pub)"
log "приватный ключ израильского узла:        /etc/backhaul/wg1_il.key (перенести на узел безопасно)"
log "SSH-туннель: инициирует RuVDS. Его публичный ключ пропишет push.sh."
