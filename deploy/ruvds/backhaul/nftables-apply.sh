#!/usr/bin/env bash
# nftables на RuVDS: default deny на input, наружу открыты только те порты,
# которые реально нужны телефонам.
#
# Отдельный скрипт, а не часть install.sh, по одной причине: неверное правило
# фаервола на удалённой машине — это потеря доступа. Здесь есть страховка:
# сначала ставится таймер отката, и только потом применяются правила. Если в
# течение CONFIRM_SEC не выполнить `./nftables-apply.sh --confirm`, старый
# ruleset вернётся сам.
#
#   ./nftables-apply.sh /etc/backhaul/params.env      # применить с откатом
#   ./nftables-apply.sh --confirm                     # подтвердить, снять откат
#   ./nftables-apply.sh --rollback                    # откатить немедленно
set -euo pipefail

CONFIRM_SEC="${CONFIRM_SEC:-600}"
SAVED=/etc/backhaul/nftables-before-backhaul.rules
MARK=/etc/backhaul/.nft-pending

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

case "${1:-}" in
  --confirm)
    systemctl stop backhaul-nft-rollback.timer 2>/dev/null || true
    systemctl disable backhaul-nft-rollback.timer 2>/dev/null || true
    rm -f "$MARK"
    nft list ruleset > /etc/nftables.conf
    log "правила подтверждены и сохранены в /etc/nftables.conf"
    exit 0
    ;;
  --rollback)
    [[ -r "$SAVED" ]] || { echo "нет сохранённого ruleset: $SAVED" >&2; exit 1; }
    nft flush ruleset
    nft -f "$SAVED"
    rm -f "$MARK"
    log "ruleset откачен к состоянию до backhaul"
    exit 0
    ;;
esac

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

mkdir -p /etc/backhaul

# 1. Сохраняем текущее состояние ОДИН раз: повторный запуск не должен
#    затереть эталон уже применёнными нашими же правилами.
if [[ ! -s "$SAVED" ]]; then
  nft list ruleset > "$SAVED"
  chmod 600 "$SAVED"
  log "исходный ruleset сохранён → $SAVED"
fi

# 2. Собираем список публичных портов.
PUB_TCP=()
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do PUB_TCP+=("$p"); done
# Staging-порты держим открытыми, пока идёт обкатка.
if [[ "${OPEN_STAGING:-true}" == "true" ]]; then
  for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
    PUB_TCP+=("$(( p + STAGING_PORT_OFFSET ))")
  done
fi
# Вход для израильского узла — только когда primary реально включён.
if [[ "${PRIMARY_ENABLED:-false}" == "true" ]]; then
  PUB_TCP+=("${FRP_BIND_PORT}")
fi
PUB_TCP_LIST="$(IFS=,; echo "${PUB_TCP[*]}")"

RULES=/etc/backhaul/nftables-backhaul.rules
cat > "$RULES" <<EOF
#!/usr/sbin/nft -f
# RuVDS: default deny. Сгенерировано deploy/ruvds/backhaul/nftables-apply.sh.
flush ruleset

table inet backhaul {
  chain input {
    type filter hook input priority filter; policy drop;

    ct state established,related accept
    ct state invalid drop
    iif "lo" accept

    # ICMP оставляем: без него ломается PMTU и диагностика.
    ip protocol icmp accept
    ip6 nexthdr ipv6-icmp accept

    # Административный SSH. Стоит ПЕРВЫМ среди tcp-правил намеренно:
    # потерять управление машиной нельзя ни при какой ошибке ниже.
    tcp dport ${RUVDS_SSH_PORT:-22} accept

    # Публичные порты VPN: ровно то, что прошито в профилях на телефонах.
    tcp dport { ${PUB_TCP_LIST} } accept

    counter comment "input drop"
  }

  chain forward {
    type filter hook forward priority filter; policy drop;
    ct state established,related accept
  }

  chain output {
    type filter hook output priority filter; policy accept;
  }
}
EOF
chmod 600 "$RULES"

# 3. Проверка синтаксиса ДО применения.
nft -c -f "$RULES"
log "синтаксис правил в порядке"

# 4. Таймер автоотката. Ставим ДО применения: если следующая команда отрежет
#    нас от машины, откат всё равно случится.
cat > /etc/systemd/system/backhaul-nft-rollback.service <<EOF
[Unit]
Description=Автооткат nftables, если новые правила не подтверждены

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'test -f ${MARK} && (nft flush ruleset; nft -f ${SAVED}; rm -f ${MARK}; logger -t backhaul "nftables откачены по таймауту") || true'
EOF
cat > /etc/systemd/system/backhaul-nft-rollback.timer <<EOF
[Unit]
Description=Автооткат nftables через ${CONFIRM_SEC}с без подтверждения

[Timer]
OnActiveSec=${CONFIRM_SEC}
AccuracySec=5s
Unit=backhaul-nft-rollback.service

[Install]
WantedBy=timers.target
EOF
touch "$MARK"
systemctl daemon-reload
systemctl start backhaul-nft-rollback.timer

# 5. Применяем.
nft -f "$RULES"
log "правила применены; открыты tcp: ${PUB_TCP_LIST}, ssh ${RUVDS_SSH_PORT:-22}"
warn "У вас ${CONFIRM_SEC}с. Проверьте доступ и выполните:  $0 --confirm"
warn "Если связь пропадёт — правила откатятся сами."
