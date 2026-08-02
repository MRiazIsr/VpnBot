#!/usr/bin/env bash
# Hetzner-сторона варианта E: терминация VPN для cdn.moskva.live.
#
# Сюда приходит сырой TCP из SSH-туннеля с RuVDS. Здесь и только здесь
# расшифровывается TLS и живут ключи. На RuVDS не попадает ничего.
#
#   Клиент → RuVDS:443 (SNI cdn.moskva.live) → ssh -L → сюда
#              ├─ /<секретный путь> → sing-box VLESS+WS
#              └─ всё остальное     → копия сайта (fallback для проверяющего)
#
# Всё слушает на loopback: снаружи Hetzner эти порты недоступны, попасть
# на них можно только через туннель.
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

: "${CDN_DOMAIN:=cdn.moskva.live}"
: "${CDN_TLS_PORT:=21443}"
: "${CDN_ACME_PORT:=80}"
: "${CDN_WS_PORT:=21081}"
: "${CDN_WS_PATH:?задайте CDN_WS_PATH в params.env}"
: "${CDN_UUID:?задайте CDN_UUID в params.env}"

STAMP="$(date -u +%Y%m%d-%H%M%S)"
BACKUP_DIR="/root/backhaul-backups/${STAMP}"
mkdir -p "$BACKUP_DIR"
log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

backup() { [[ -e "$1" ]] && install -D -m 0600 "$1" "${BACKUP_DIR}$1" && log "бэкап: $1"; return 0; }

# ── sing-box: VLESS поверх WebSocket, без собственного TLS ──
# TLS снимает Caddy. Отдельный инстанс, а не правка /etc/sing-box/config.json:
# основной конфиг перегенерируется vpnbot'ом и затёр бы правки.
log "sing-box-cdn на 127.0.0.1:${CDN_WS_PORT}"
mkdir -p /etc/sing-box-cdn
backup /etc/sing-box-cdn/config.json
cat > /etc/sing-box-cdn/config.json <<EOF
{
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [
    {
      "type": "vless",
      "tag": "cdn-in",
      "listen": "127.0.0.1",
      "listen_port": ${CDN_WS_PORT},
      "users": [ { "uuid": "${CDN_UUID}", "name": "cdn" } ],
      "transport": { "type": "ws", "path": "${CDN_WS_PATH}" }
    }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "route": { "rules": [], "final": "direct" }
}
EOF
chmod 600 /etc/sing-box-cdn/config.json

SB_BIN="$(command -v sing-box || echo /usr/local/bin/sing-box)"
"$SB_BIN" check -c /etc/sing-box-cdn/config.json
log "sing-box check: OK"

backup /etc/systemd/system/sing-box-cdn.service
cat > /etc/systemd/system/sing-box-cdn.service <<EOF
[Unit]
Description=sing-box: VPN-инбаунд для ${CDN_DOMAIN} (за туннелем с RuVDS)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${SB_BIN} run -c /etc/sing-box-cdn/config.json
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
StateDirectory=sing-box-cdn
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

[Install]
WantedBy=multi-user.target
EOF

# ── копия сайта: fallback для проверяющего ──
# Статика, поэтому просто копируем то же, что собирается на RuVDS.
log "копия сайта-заглушки → /var/www/${CDN_DOMAIN}"
mkdir -p "/var/www/${CDN_DOMAIN}"
if [[ -d /opt/VpnBot/deploy/decoy-site ]]; then
  python3 /opt/VpnBot/deploy/decoy-site/build.py \
    --out "/var/www/${CDN_DOMAIN}" --cache /var/lib/moskva-live || \
    log "сборка сайта не удалась — положите статику вручную"
fi

# ── Caddy: отдельный конфиг, подключается к существующему ──
log "Caddy: сайт ${CDN_DOMAIN} на 127.0.0.1:${CDN_TLS_PORT}"
CADDY_SNIPPET=/etc/caddy/cdn-moskva.caddy
backup "$CADDY_SNIPPET"
cat > "$CADDY_SNIPPET" <<EOF
# Слушаем только на loopback: снаружи Hetzner этого нет, приходит
# исключительно из SSH-туннеля с RuVDS.
https://${CDN_DOMAIN}:${CDN_TLS_PORT} {
	bind 127.0.0.1

	# TLS-ALPN здесь невозможен: :443 на этой машине занят основным sing-box,
	# и до нас челлендж не дойдёт. Остаётся HTTP-01 — он приезжает через
	# туннель на :${CDN_ACME_PORT}, куда RuVDS проксирует /.well-known/.
	tls {
		issuer acme {
			disable_tlsalpn_challenge
		}
	}

	# VPN. Путь секретный: кто его не знает, увидит сайт.
	handle ${CDN_WS_PATH} {
		reverse_proxy 127.0.0.1:${CDN_WS_PORT}
	}

	# Всё прочее — обычный сайт. Проверяющий, дошедший сюда, получит
	# страницу с валидным сертификатом, а не обрыв.
	handle {
		root * /var/www/${CDN_DOMAIN}
		file_server
	}

	log {
		output file /var/log/caddy/cdn-moskva.log
		level WARN
	}
}
EOF

CADDYFILE=/etc/caddy/Caddyfile
backup "$CADDYFILE"
if ! grep -q "cdn-moskva.caddy" "$CADDYFILE"; then
  printf '\nimport %s\n' "$CADDY_SNIPPET" >> "$CADDYFILE"
fi

if ! caddy validate --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1; then
  echo "Caddyfile не проходит validate — откатываем" >&2
  cp "${BACKUP_DIR}${CADDYFILE}" "$CADDYFILE"
  exit 1
fi

systemctl daemon-reload
systemctl enable --now sing-box-cdn.service
systemctl reload caddy || systemctl restart caddy

log "готово. Бэкапы: ${BACKUP_DIR}"
log "туннель с RuVDS должен пробрасывать:"
log "  RuVDS:14443 → 127.0.0.1:${CDN_TLS_PORT}   (VPN + сайт)"
log "  RuVDS:14480 → 127.0.0.1:${CDN_ACME_PORT}  (ACME для ${CDN_DOMAIN})"
