#!/usr/bin/env bash
# Перевод боевых портов RuVDS со старого DNAT на relay — с автооткатом.
#
# До этого шага relay живёт на staging-портах и прод не задет вообще.
# Здесь мы:
#   1. снимаем снимок текущего DNAT (чтобы было чем откатываться);
#   2. регенерируем backhaul.json в боевом режиме (порты без смещения);
#   3. sing-box check;
#   4. ставим таймер автоотката;
#   5. убираем DNAT для боевых портов и перезапускаем relay;
#   6. ждём подтверждения оператора.
#
# Без `--confirm` в течение CONFIRM_SEC всё вернётся к DNAT автоматически.
#
#   ./promote.sh /etc/backhaul/params.env
#   ./promote.sh --confirm
#   ./promote.sh --rollback
set -euo pipefail

CONFIRM_SEC="${CONFIRM_SEC:-900}"
DNAT_SAVE=/etc/backhaul/dnat-before-promote.rules
CFG_SAVE=/etc/backhaul/backhaul-staging.json
MARK=/etc/backhaul/.promote-pending

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

do_rollback() {
  log "откат: возвращаем DNAT и staging-конфиг relay"
  if [[ -s "$DNAT_SAVE" ]]; then
    iptables-restore -T nat < "$DNAT_SAVE" || warn "iptables-restore не отработал"
  fi
  if [[ -s "$CFG_SAVE" ]]; then
    cp "$CFG_SAVE" /etc/vpnbot/backhaul.json
    /usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -render-relay \
      > /etc/sing-box-relay/config.json
    "${SINGBOX_RELAY_BIN:-/usr/local/bin/sing-box}" check -c /etc/sing-box-relay/config.json
    systemctl restart sing-box-relay backhaul-monitor
  fi
  rm -f "$MARK"
  log "откат завершён: трафик снова идёт через старый DNAT"
}

case "${1:-}" in
  --confirm)
    systemctl stop backhaul-promote-rollback.timer 2>/dev/null || true
    systemctl disable backhaul-promote-rollback.timer 2>/dev/null || true
    rm -f "$MARK"
    log "перевод подтверждён; DNAT для боевых портов снят окончательно"
    log "снимок старых правил остаётся в ${DNAT_SAVE}"
    exit 0
    ;;
  --rollback)
    do_rollback
    exit 0
    ;;
esac

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

# Не переводим прод, пока не убедились, что хотя бы один backhaul живой.
log "проверка backhaul'ов перед переводом"
if ! /usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -probe; then
  warn "не все backhaul'ы прошли проверку — см. вывод выше"
  read -r -p "Всё равно переводить прод? [y/N] " ans
  [[ "$ans" == "y" || "$ans" == "Y" ]] || exit 1
fi

log "снимок текущих правил NAT → ${DNAT_SAVE}"
iptables-save -t nat > "$DNAT_SAVE"
chmod 600 "$DNAT_SAVE"
cp /etc/vpnbot/backhaul.json "$CFG_SAVE"

log "регенерация backhaul.json в боевом режиме"
MODE=production "$(dirname "$0")/render-config.sh" "$PARAMS"

log "sing-box check"
"${SINGBOX_RELAY_BIN:-/usr/local/bin/sing-box}" check -c /etc/sing-box-relay/config.json

# Таймер отката ставим ДО того, как трогать прод.
cat > /etc/systemd/system/backhaul-promote-rollback.service <<EOF
[Unit]
Description=Автооткат перевода боевых портов на relay

[Service]
Type=oneshot
ExecStart=$(readlink -f "$0") --rollback
EOF
cat > /etc/systemd/system/backhaul-promote-rollback.timer <<EOF
[Unit]
Description=Автооткат promote через ${CONFIRM_SEC}с без подтверждения

[Timer]
OnActiveSec=${CONFIRM_SEC}
AccuracySec=5s
Unit=backhaul-promote-rollback.service

[Install]
WantedBy=timers.target
EOF
touch "$MARK"
systemctl daemon-reload
systemctl start backhaul-promote-rollback.timer

log "снимаем DNAT для боевых портов"
HETZNER_TARGET="${HETZNER_IP}"
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
  # Старая схема жила и в iptables (portforward.go), и в nftables
  # (table ip relay). Чистим обе, молча: чего нет — того нет.
  iptables -t nat -D PREROUTING -p tcp --dport "$p" -j DNAT \
    --to-destination "${HETZNER_TARGET}:${p}" 2>/dev/null || true
  iptables -t nat -D POSTROUTING -d "${HETZNER_TARGET}" -p tcp --dport "$p" \
    -j MASQUERADE 2>/dev/null || true
  nft delete rule ip relay prerouting handle \
    "$(nft -a list table ip relay 2>/dev/null | awk -v p="$p" '/dport '"$p"' dnat/ {print $NF; exit}')" 2>/dev/null || true
done

log "перезапуск relay на боевых портах"
systemctl restart sing-box-relay
sleep 2
systemctl restart backhaul-monitor
systemctl --no-pager --lines=5 status sing-box-relay || true

warn "У вас ${CONFIRM_SEC}с на проверку живым клиентом."
warn "Всё хорошо →  $0 --confirm"
warn "Что-то не так → $0 --rollback   (или просто ничего не делайте: откатится само)"
