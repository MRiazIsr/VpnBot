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

# Метка неподтверждённого перевода: снимаем, чтобы nftables-apply.sh и другие
# скрипты не считали, что где-то ещё идёт незавершённый promote.
rm -f /etc/backhaul/.promote-pending

log "останавливаем юниты backhaul"
for u in backhaul-monitor sing-box-relay sing-box-bh2 frps backhaul-fssh@vless backhaul-fssh@mtproto; do
  systemctl disable --now "${u%.service}.service" >/dev/null 2>&1 || true
done
systemctl stop backhaul-nft-rollback.timer backhaul-promote-rollback.timer >/dev/null 2>&1 || true
systemctl disable backhaul-nft-rollback.timer backhaul-promote-rollback.timer >/dev/null 2>&1 || true

# Правила фаервола и DNAT возвращаются ОДНИМ решением, а не двумя шагами.
#
# Здесь была ровно та дыра, которую задача 1 чинила в promote.sh, только в
# скрипте, который док называет рычагом «откатить всё»:
#   * читался /etc/backhaul/dnat-before-promote.rules — файл, который никто
#     больше не пишет (promote.sh пишет relay-nat.nft), то есть основная ветка
#     восстановления была мертва;
#   * запасная ветка звала `iptables -t nat -A`, а iptables на этой машине
#     собран как legacy и таблицы ip relay не видит вовсе — восстановление
#     было бы no-op с бодрым сообщением об успехе;
#   * список портов брался из RELAY_*_PORTS, где сейчас 6 портов из 11
#     боевых: даже сработай оно, вернулось бы меньше половины правил.
SAVED_RULESET=/etc/backhaul/nftables-before-backhaul.rules
RELAY_SNAPSHOT=/etc/backhaul/relay-nat.nft

log "возвращаем правила фаервола и DNAT"
if [[ -s "$SAVED_RULESET" ]]; then
  # Полный ruleset «до backhaul» — в нём и фаервол, и боевая table ip relay
  # с DNAT. Одной транзакцией: flush и восстановление разными вызовами nft
  # оставили бы машину без правил в промежутке.
  { echo "flush ruleset"; cat "$SAVED_RULESET"; } | nft -f -
  log "ruleset откачен к состоянию до backhaul (включая table ip relay с DNAT)"
elif [[ -s "$RELAY_SNAPSHOT" ]]; then
  warn "нет ${SAVED_RULESET}: возвращаю только table ip relay из снимка promote.sh"
  { printf 'add table ip relay\ndelete table ip relay\n'; cat "$RELAY_SNAPSHOT"; } | nft -f -
  log "table ip relay восстановлена из ${RELAY_SNAPSHOT}"
  warn "правила фаервола (table inet backhaul) остались как есть — снимка до backhaul нет"
else
  echo "ОТКАЗ: восстанавливать не из чего." >&2
  echo "  Нет ни ${SAVED_RULESET}, ни ${RELAY_SNAPSHOT}." >&2
  echo "  Боевое состояние возвращается аварийным рычагом:" >&2
  echo "    systemctl restart nftables" >&2
  echo "  (nftables.service = flush ruleset + nft -f /etc/nftables.conf, а там" >&2
  echo "   все три боевые таблицы: ip relay, ip mss_clamp, inet mtproxy_smart_syn_alt)." >&2
  echo "  Юниты backhaul уже остановлены; после рычага проверьте:" >&2
  echo "    nft list table ip relay | grep -cE 'dnat|redirect'" >&2
  exit 1
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

log "готово. Проверьте:"
log "  nft list table ip relay | grep -cE 'dnat|redirect'   # боевых правил должно быть 11"
log "  nft list ruleset | grep -c 'redirect to :3'          # relay-редиректов должно не остаться"
log "  и живой клиент на боевом порту."
