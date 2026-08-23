#!/usr/bin/env bash
# Доставка backhaul на RuVDS. Запускать НА HETZNER — управляющий канал к RuVDS
# живёт там же, где и остальной код vpnbot.
#
#   ./push.sh /etc/backhaul/params.env            # положить файлы, ничего не запускать
#   ./push.sh /etc/backhaul/params.env --install  # положить и выполнить install.sh
#
# Секреты (params.env) уезжают по SSH и никогда не попадают в репозиторий.
set -euo pipefail

PARAMS="${1:?укажите params.env}"
DO_INSTALL="${2:-}"
# shellcheck disable=SC1090
source "$PARAMS"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

SRC="$(cd "$(dirname "$0")" && pwd)"
SSH_OPTS=(-o ConnectTimeout=15 -o BatchMode=yes -o StrictHostKeyChecking=yes
          -i "${RUVDS_SSH_KEY_PATH:-/root/.ssh/ruvds_key}" -p "${RUVDS_SSH_PORT:-22}")
DEST="${RUVDS_SSH_USER:-root}@${RUVDS_IP}"

log "проверка доступности RuVDS"
if ! ssh "${SSH_OPTS[@]}" "$DEST" 'echo ok' >/dev/null 2>&1; then
  echo "RuVDS недоступен по SSH (${RUVDS_IP}:${RUVDS_SSH_PORT:-22}) — доставка невозможна" >&2
  exit 1
fi

log "собираем backhaul-monitor под RuVDS"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
if [[ -x /opt/VpnBot/bin/backhaul-monitor ]]; then
  cp /opt/VpnBot/bin/backhaul-monitor "$BUILD_DIR/"
else
  ( cd /opt/VpnBot && GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/backhaul-monitor" ./cmd/backhaul-monitor )
fi

log "копируем на RuVDS"
ssh "${SSH_OPTS[@]}" "$DEST" 'mkdir -p /root/backhaul-deploy /etc/backhaul && chmod 700 /etc/backhaul'
scp "${SSH_OPTS[@]}" -q \
  "$SRC/install.sh" "$SRC/render-config.sh" "$SRC/nftables-apply.sh" \
  "$SRC/promote.sh" "$SRC/uninstall.sh" "$BUILD_DIR/backhaul-monitor" \
  "$DEST:/root/backhaul-deploy/"
# rollback-drill.sh едет вместе с остальным намеренно: доказательство отката
# должно быть на машине ДО того, как на откат начнут полагаться.
scp "${SSH_OPTS[@]}" -q "$SRC/../../../scripts/backhaul/verify.sh" \
  "$SRC/../../../scripts/backhaul/switch.sh" \
  "$SRC/../../../scripts/backhaul/rollback-drill.sh" "$DEST:/root/backhaul-deploy/"
scp "${SSH_OPTS[@]}" -q "$PARAMS" "$DEST:/etc/backhaul/params.env"
ssh "${SSH_OPTS[@]}" "$DEST" 'chmod 600 /etc/backhaul/params.env; chmod +x /root/backhaul-deploy/*.sh /root/backhaul-deploy/backhaul-monitor'

if [[ "$DO_INSTALL" == "--install" ]]; then
  log "запускаем install.sh на RuVDS"
  ssh "${SSH_OPTS[@]}" "$DEST" 'cd /root/backhaul-deploy && ./install.sh /etc/backhaul/params.env'

  # Направление SSH-туннеля инвертировано: клиент — RuVDS, сервер — Hetzner.
  # Значит ключ рождается на RuVDS, а публичная часть должна приехать сюда.
  log "забираем публичный ключ туннеля с RuVDS и прописываем его здесь"
  PUB="$(ssh "${SSH_OPTS[@]}" "$DEST" "cat ${SSH_KEY_PATH}.pub" 2>/dev/null || true)"
  if [[ -n "$PUB" ]]; then
    install -d -m 0700 -o "${SSH_USER}" -g "${SSH_USER}" "/var/lib/${SSH_USER}/.ssh"
    printf '%s\n' "$PUB" > "/var/lib/${SSH_USER}/.ssh/authorized_keys"
    chown "${SSH_USER}:${SSH_USER}" "/var/lib/${SSH_USER}/.ssh/authorized_keys"
    chmod 600 "/var/lib/${SSH_USER}/.ssh/authorized_keys"
    log "ключ прописан; перезапускаем туннель на RuVDS"
    ssh "${SSH_OPTS[@]}" "$DEST" 'systemctl restart backhaul-fssh@vless backhaul-fssh@mtproto || true'
    sleep 3
    ssh "${SSH_OPTS[@]}" "$DEST" 'ss -tlnH | grep -E ":(11082|11092)$" | sed "s/^/  SOCKS поднят: /" || echo "  SOCKS не поднялся — см. journalctl -u backhaul-fssh@vless"'
  else
    warn "не удалось забрать ${SSH_KEY_PATH}.pub с RuVDS"
  fi
else

  log "файлы доставлены. Дальше на RuVDS:"
  echo "    cd /root/backhaul-deploy && ./install.sh /etc/backhaul/params.env"
  echo "    ./nftables-apply.sh /etc/backhaul/params.env   # затем --confirm"
  echo "    ./verify.sh /etc/backhaul/params.env"
  echo "    ./rollback-drill.sh                            # доказать откат ДО перевода"
  echo "    ./promote.sh /etc/backhaul/params.env --only 2058   # канарейка, затем --confirm"
fi
