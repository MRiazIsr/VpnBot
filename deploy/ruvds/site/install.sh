#!/usr/bin/env bash
# RuVDS-сторона варианта E: сайт-приманка + SNI-маршрутизация + туннель.
#
#   Клиент → :443
#     ├── SNI moskva.live      → локальный сайт (TLS терминируется здесь)
#     └── SNI cdn.moskva.live  → ssh -L → Hetzner (TLS терминируется ТАМ)
#
# Эта машина держит сертификат только сайта-приманки. Ключей VPN здесь нет,
# расшифрованного пользовательского трафика — тоже: ssl_preread читает имя
# из ClientHello, не вскрывая соединение.
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

: "${SITE_DOMAIN:=moskva.live}"
: "${CDN_DOMAIN:=cdn.moskva.live}"
: "${CDN_TLS_PORT:=21443}"
: "${CDN_ACME_PORT:=80}"
: "${ACME_EMAIL:?задайте ACME_EMAIL в params.env}"
: "${HETZNER_IP:?}"
: "${SSH_USER:=backhaul}"
: "${SSH_KEY_PATH:=/etc/backhaul/bh_ed25519}"

SRC="$(cd "$(dirname "$0")" && pwd)"
STAMP="$(date -u +%Y%m%d-%H%M%S)"
BACKUP_DIR="/root/backhaul-backups/${STAMP}"
mkdir -p "$BACKUP_DIR" /etc/backhaul /var/www/acme /var/lib/moskva-live
log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
backup() { [[ -e "$1" ]] && install -D -m 0600 "$1" "${BACKUP_DIR}$1" && log "бэкап: $1"; return 0; }

log "пакеты"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y nginx certbot python3 >/dev/null

# ── 1. Туннель на Hetzner ──
# Поднимаем ПЕРВЫМ: без него не пройдёт ACME для cdn-поддомена.
log "SSH-туннель RuVDS → Hetzner"
[[ -f "$SSH_KEY_PATH" ]] || {
  ssh-keygen -t ed25519 -N '' -C 'moskva-tunnel' -f "$SSH_KEY_PATH" >/dev/null
  warn "сгенерирован ключ. Публичную часть добавьте на Hetzner пользователю ${SSH_USER}:"
  cat "${SSH_KEY_PATH}.pub"
}
[[ -s /etc/backhaul/known_hosts ]] || \
  ssh-keyscan -T 10 -p "${SSH_PORT:-22}" "$HETZNER_IP" > /etc/backhaul/known_hosts 2>/dev/null || \
  warn "не удалось получить ключ хоста Hetzner"
chmod 600 "$SSH_KEY_PATH" /etc/backhaul/known_hosts 2>/dev/null || true

backup /etc/systemd/system/moskva-tunnel.service
cat > /etc/systemd/system/moskva-tunnel.service <<EOF
[Unit]
Description=SSH-туннель RuVDS → Hetzner (VPN + ACME для ${CDN_DOMAIN})
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=/usr/bin/ssh -NT \\
  -o ExitOnForwardFailure=yes \\
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o TCPKeepAlive=yes \\
  -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/etc/backhaul/known_hosts \\
  -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 \\
  -o ForwardAgent=no -o ForwardX11=no -o RequestTTY=no \\
  -o ControlMaster=no -o ControlPath=none \\
  -i ${SSH_KEY_PATH} -p ${SSH_PORT:-22} \\
  -L 127.0.0.1:14443:127.0.0.1:${CDN_TLS_PORT} \\
  -L 127.0.0.1:14480:127.0.0.1:${CDN_ACME_PORT} \\
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

# ── 2. Сайт ──
log "сайт-заглушка"
install -D -m 0755 "$SRC/../../decoy-site/build.py" /usr/local/bin/moskva-build
mkdir -p /usr/local/share/moskva-live
cp -r "$SRC/../../decoy-site/pages" "$SRC/../../decoy-site/static" /usr/local/share/moskva-live/
mkdir -p /etc/moskva-live
[[ -f /etc/moskva-live/feeds.conf ]] || cat > /etc/moskva-live/feeds.conf <<'EOF'
# Ленты для сайта. Формат: Название|URL. Берутся только заголовки и ссылки.
# Сборка идёт с российского адреса, поэтому городские источники доступны.
Москва 24|https://www.m24.ru/rss.xml
Мэр Москвы|https://www.mos.ru/rss/
EOF

backup /etc/systemd/system/moskva-build.service
cat > /etc/systemd/system/moskva-build.service <<'EOF'
[Unit]
Description=Пересборка сайта moskva.live
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/moskva-build --out /var/www/moskva.live --cache /var/lib/moskva-live
Nice=10
EOF
cat > /etc/systemd/system/moskva-build.timer <<'EOF'
[Unit]
Description=Пересборка moskva.live каждые 30 минут

[Timer]
OnBootSec=3min
OnUnitActiveSec=30min
RandomizedDelaySec=5min
Persistent=true

[Install]
WantedBy=timers.target
EOF

# ── 3. nginx ──
log "nginx: SNI-маршрутизация и сайт"
backup /etc/nginx/nginx.conf
install -m 0644 "$SRC/nginx-stream.conf" /etc/backhaul/nginx-stream.conf
install -m 0644 "$SRC/nginx-site.conf"   /etc/nginx/sites-available/moskva.conf
ln -sf /etc/nginx/sites-available/moskva.conf /etc/nginx/sites-enabled/moskva.conf
rm -f /etc/nginx/sites-enabled/default

# stream живёт на верхнем уровне, внутрь http его положить нельзя
grep -q 'nginx-stream.conf' /etc/nginx/nginx.conf || \
  printf '\ninclude /etc/backhaul/nginx-stream.conf;\n' >> /etc/nginx/nginx.conf

# ── 4. Сертификат сайта ──
# Домен указывает сюда, поэтому HTTP-01 проходит локально. Стартуем nginx
# во временной конфигурации без ssl, иначе он не поднимется без сертификата.
if [[ ! -d "/etc/letsencrypt/live/${SITE_DOMAIN}" ]]; then
  log "выпуск сертификата для ${SITE_DOMAIN}"
  rm -f /etc/nginx/sites-enabled/moskva.conf
  nginx -t && systemctl restart nginx
  certbot certonly --webroot -w /var/www/acme \
    -d "${SITE_DOMAIN}" -d "www.${SITE_DOMAIN}" \
    --agree-tos -m "${ACME_EMAIL}" --non-interactive || \
    warn "certbot не отработал — проверьте A-записи ${SITE_DOMAIN} и www"
  ln -sf /etc/nginx/sites-available/moskva.conf /etc/nginx/sites-enabled/moskva.conf
fi

log "первая сборка сайта"
/usr/local/bin/moskva-build --out /var/www/moskva.live --cache /var/lib/moskva-live || \
  warn "сборка не удалась — сайт будет пуст"

log "проверка конфигурации nginx"
nginx -t

systemctl daemon-reload
systemctl enable --now moskva-tunnel.service
systemctl enable --now moskva-build.timer
systemctl restart nginx

log "готово. Бэкапы: ${BACKUP_DIR}"
echo
log "проверить (из России):"
echo "    curl -sSI https://${SITE_DOMAIN}/ | head -1        # сайт"
echo "    openssl s_client -connect 127.0.0.1:443 -servername ${CDN_DOMAIN} </dev/null 2>&1 | head -5"
echo "    systemctl status moskva-tunnel --no-pager"
