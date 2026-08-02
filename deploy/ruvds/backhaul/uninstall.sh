#!/usr/bin/env bash
# Полный откат backhaul на RuVDS к состоянию «до».
#
# Возвращает старую схему: DNAT RuVDS → Hetzner, старые правила фаервола,
# ни одного нашего юнита. Пользователи ничего не замечают — публичные порты
# и профили те же самые.
#
#   ./uninstall.sh /etc/backhaul/params.env
#   ./uninstall.sh /etc/backhaul/params.env --purge   # снести ещё и конфиги/ключи
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
PURGE="${2:-}"
# shellcheck disable=SC1090
source "$PARAMS"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

log "останавливаем юниты backhaul"
for u in backhaul-monitor sing-box-relay sing-box-bh2 frps backhaul-fssh@vless backhaul-fssh@mtproto; do
  systemctl disable --now "${u%.service}.service" >/dev/null 2>&1 || true
done
systemctl stop backhaul-nft-rollback.timer backhaul-promote-rollback.timer >/dev/null 2>&1 || true
systemctl disable backhaul-nft-rollback.timer backhaul-promote-rollback.timer >/dev/null 2>&1 || true

log "возвращаем правила фаервола"
if [[ -s /etc/backhaul/nftables-before-backhaul.rules ]]; then
  nft flush ruleset
  nft -f /etc/backhaul/nftables-before-backhaul.rules
  log "nftables откачены к сохранённому состоянию"
else
  warn "нет /etc/backhaul/nftables-before-backhaul.rules — nftables не трогаем"
fi

log "возвращаем DNAT"
if [[ -s /etc/backhaul/dnat-before-promote.rules ]]; then
  iptables-restore -T nat < /etc/backhaul/dnat-before-promote.rules
  log "таблица nat восстановлена из снимка"
else
  warn "снимка DNAT нет — восстанавливаем правила по списку портов"
  for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
    iptables -t nat -C PREROUTING -p tcp --dport "$p" -j DNAT \
      --to-destination "${HETZNER_IP}:${p}" 2>/dev/null || \
    iptables -t nat -A PREROUTING -p tcp --dport "$p" -j DNAT \
      --to-destination "${HETZNER_IP}:${p}"
    iptables -t nat -C POSTROUTING -d "${HETZNER_IP}" -p tcp --dport "$p" -j MASQUERADE 2>/dev/null || \
    iptables -t nat -A POSTROUTING -d "${HETZNER_IP}" -p tcp --dport "$p" -j MASQUERADE
  done
fi

# Дропина sshd здесь нет: направление туннеля инвертировано, ограничения
# живут на Hetzner. Здесь только клиентские юниты.
log "останавливаем SSH-туннель"
systemctl disable --now backhaul-fssh@vless.service backhaul-fssh@mtproto.service >/dev/null 2>&1 || true

if [[ "$PURGE" == "--purge" ]]; then
  warn "--purge: удаляем конфиги, состояние и ключи туннеля"
  rm -rf /etc/sing-box-relay /etc/sing-box-bh2 /etc/vpnbot/backhaul.json \
         /var/lib/vpnbot/backhaul-state.json /var/lib/sing-box-relay
  rm -f /etc/systemd/system/{backhaul-monitor,sing-box-relay,sing-box-bh2,frps}.service
  rm -f /etc/systemd/system/backhaul-fssh@.service /etc/backhaul/fssh-*.env
  rm -f /etc/systemd/system/backhaul-{nft,promote}-rollback.{service,timer}
  systemctl daemon-reload
  warn "бэкапы в /root/backhaul-backups сохранены — они не удаляются"
fi

log "готово. Проверьте: ss -tlnp | grep -E ':(2059|9443)' и живой клиент."
