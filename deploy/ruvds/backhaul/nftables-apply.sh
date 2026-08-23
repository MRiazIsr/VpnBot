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
PROMOTE_MARK=/etc/backhaul/.promote-pending

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

# Живые правила `redirect to :3xxxx` означают, что боевые порты уже заведены на
# relay. Сохранять такое состояние в /etc/nftables.conf нельзя: этот файл —
# аварийный рычаг (`systemctl restart nftables` возвращает боевой DNAT), и
# записать в него переведённое состояние значит уничтожить сам рычаг.
# Диапазон 30000-99999 намеренно шире смещения по умолчанию (+30000): если
# STAGING_PORT_OFFSET однажды поменяют, проверка не должна перестать замечать
# переведённые порты. Боевой redirect MTProto (:29443) в диапазон не попадает —
# он не относится к relay и подтверждению фаервола не мешает.
relay_redirects_live() {
  nft list ruleset 2>/dev/null | grep -qE 'redirect to :[3-9][0-9]{4}([^0-9]|$)'
}

case "${1:-}" in
  --confirm)
    if [[ -e "$PROMOTE_MARK" ]]; then
      echo "ОТКАЗ: идёт неподтверждённый перевод портов на relay (${PROMOTE_MARK})." >&2
      echo "  Сначала promote.sh --confirm или promote.sh --rollback." >&2
      exit 1
    fi
    if relay_redirects_live; then
      echo "ОТКАЗ: в ruleset живы правила redirect на relay-порты (:3xxxx)." >&2
      echo "  Записать их в /etc/nftables.conf нельзя: этот файл — аварийный рычаг," >&2
      echo "  из которого 'systemctl restart nftables' возвращает боевой DNAT." >&2
      echo "  Правила фаервола уже применены и работают; таймер отката снимаю," >&2
      echo "  но /etc/nftables.conf не трогаю." >&2
      systemctl stop backhaul-nft-rollback.timer 2>/dev/null || true
      systemctl disable backhaul-nft-rollback.timer 2>/dev/null || true
      rm -f "$MARK"
      exit 1
    fi
    systemctl stop backhaul-nft-rollback.timer 2>/dev/null || true
    systemctl disable backhaul-nft-rollback.timer 2>/dev/null || true
    rm -f "$MARK"
    nft list ruleset > /etc/nftables.conf
    log "правила подтверждены и сохранены в /etc/nftables.conf"
    exit 0
    ;;
  --rollback)
    [[ -r "$SAVED" ]] || { echo "нет сохранённого ruleset: $SAVED" >&2; exit 1; }
    # Одной транзакцией: flush + восстановление. Два отдельных вызова nft — это
    # промежуток, в котором машина стоит вовсе без правил.
    { echo "flush ruleset"; cat "$SAVED"; } | nft -f -
    rm -f "$MARK"
    log "ruleset откачен к состоянию до backhaul"
    exit 0
    ;;
esac

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

# Перевод портов на relay и правка фаервола — два независимых ползучих
# изменения с двумя независимыми таймерами отката. Наложить одно на другое
# значит получить два отката, которые срабатывают вразнобой и восстанавливают
# несогласованные куски состояния. Пока перевод не подтверждён или не откачен,
# фаервол не трогаем.
if [[ -e "$PROMOTE_MARK" ]]; then
  echo "ОТКАЗ: идёт неподтверждённый перевод портов на relay (${PROMOTE_MARK})." >&2
  echo "  Сначала доведите его до конца: promote.sh --confirm или promote.sh --rollback." >&2
  exit 1
fi

# Списки relay могут быть пустыми (RELAY_MTPROTO_PORTS сейчас пуст намеренно).
RELAY_VLESS_PORTS="${RELAY_VLESS_PORTS:-}"
RELAY_MTPROTO_PORTS="${RELAY_MTPROTO_PORTS:-}"

mkdir -p /etc/backhaul

# 1. Сохраняем текущее состояние ОДИН раз: повторный запуск не должен
#    затереть эталон уже применёнными нашими же правилами.
if [[ ! -s "$SAVED" ]]; then
  nft list ruleset > "$SAVED"
  chmod 600 "$SAVED"
  log "исходный ruleset сохранён → $SAVED"
fi

# 2. Собираем список публичных портов.
#
# Источник — PUBLIC_TCP_PORTS, а НЕ RELAY_*_PORTS. Это разные списки:
# RELAY_*_PORTS — то, что переведено на relay (подмножество), PUBLIC_TCP_PORTS —
# всё, что RuVDS обязан принимать. Собрать default-deny фаервол из relay-списка
# значит закрыть все остальные боевые профили (443, RU/RU-TCP/RU-STLS, 8445,
# прямые 4443/8444/8447) — то есть уронить их молча, правилом policy drop.
PUBLIC_TCP_PORTS="${PUBLIC_TCP_PORTS:-}"
if [[ -z "${PUBLIC_TCP_PORTS// /}" ]]; then
  echo "ОТКАЗ: PUBLIC_TCP_PORTS не задан в ${PARAMS}." >&2
  echo "  Это полный список публичных TCP-портов RuVDS; без него ruleset с" >&2
  echo "  policy drop закрыл бы все профили. Возьмите список из" >&2
  echo "  deploy/backhaul/params.env.example." >&2
  exit 1
fi

PUB_TCP=()
for p in ${PUBLIC_TCP_PORTS}; do PUB_TCP+=("$p"); done
# Relay-порты — на случай, если список публичных портов кто-то подрезал:
# порт, переведённый на relay, обязан быть открыт в любом случае.
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do PUB_TCP+=("$p"); done
# Смещённые порты relay открыты ВСЕГДА, а не только на время обкатки.
# Две причины, и вторая важнее первой:
#   1. на них ходят проверки staging'а живым клиентом;
#   2. `redirect to :3xxxx` переписывает адрес назначения в prerouting, то есть
#      ДО input. Цепочка input видит уже смещённый номер порта, и без него в
#      списке policy drop убил бы ровно тот трафик, который мы только что
#      перевели на relay.
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
  PUB_TCP+=("$(( p + STAGING_PORT_OFFSET ))")
done
# Вход для израильского узла — только когда primary реально включён.
if [[ "${PRIMARY_ENABLED:-false}" == "true" ]]; then
  PUB_TCP+=("${FRP_BIND_PORT}")
fi
# Дубликаты обязательны к удалению: анонимное множество nft с повторяющимся
# элементом не применяется целиком («File exists»).
PUB_TCP_SORTED=$(printf '%s\n' "${PUB_TCP[@]}" | sort -un)
PUB_TCP_LIST="$(echo "$PUB_TCP_SORTED" | paste -sd, -)"

RULES=/etc/backhaul/nftables-backhaul.rules
cat > "$RULES" <<EOF
#!/usr/sbin/nft -f
# RuVDS: default deny. Сгенерировано deploy/ruvds/backhaul/nftables-apply.sh.
#
# НЕТ 'flush ruleset'. Раньше он тут был, и это была самая опасная строка в
# репозитории: на машине живут три боевые таблицы — ip relay (11 правил
# dnat/redirect, весь трафик RuVDS→Hetzner), ip mss_clamp и
# inet mtproxy_smart_syn_alt. Флеш сносил их все, а док велел запускать этот
# скрипт прямо перед promote.sh.
#
# Вместо флеша — атомарная замена ТОЛЬКО своей таблицы: add (чтобы delete не
# упал на первом запуске) + delete + полное определение. Всё это один батч
# nft, то есть одна транзакция ядра; чужие таблицы не затрагиваются.
add table inet backhaul
delete table inet backhaul

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

    # Боевой путь RuVDS→Hetzner — это DNAT в table ip relay: пакет
    # переписывается в prerouting и дальше идёт НЕ в input, а в forward, в
    # состоянии NEW. Без этого accept default deny убил бы разом все порты,
    # которые ещё сидят на DNAT (8445 и всё, что не переведено на relay),
    # причём тихо — порт бы просто перестал отвечать.
    # 'ct status dnat' сужает разрешение ровно до того, что оттранслировала
    # наша же таблица nat: произвольный транзитный трафик по-прежнему drop.
    ct status dnat accept
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
ExecStart=/bin/sh -c 'test -f ${MARK} || exit 0; { echo "flush ruleset"; cat ${SAVED}; } | nft -f -; rm -f ${MARK}; logger -t backhaul "nftables откачены по таймауту"'
EOF
cat > /etc/systemd/system/backhaul-nft-rollback.timer <<EOF
[Unit]
Description=Автооткат nftables через ${CONFIRM_SEC}с без подтверждения

[Timer]
OnActiveSec=${CONFIRM_SEC}
AccuracySec=5s
# Без этого отработавший таймер остаётся active навсегда, и следующий
# 'systemctl start' оказывается no-op: скрипт бы доложил, что откат взведён,
# а взведено бы ничего не было.
RemainAfterElapse=no
Unit=backhaul-nft-rollback.service

[Install]
WantedBy=timers.target
EOF
touch "$MARK"
systemctl daemon-reload
# restart, а не start: start на уже активном таймере ничего не делает и
# оставляет тикать чужой, более старый отсчёт.
systemctl restart backhaul-nft-rollback.timer

# Таймер — единственная страховка от потери доступа к машине. Не взведён —
# правила не применяем.
if ! systemctl is-active --quiet backhaul-nft-rollback.timer; then
  rm -f "$MARK"
  echo "ОТКАЗ: таймер автоотката не взведён — правила не применялись." >&2
  echo "  Проверьте: systemctl status backhaul-nft-rollback.timer" >&2
  exit 1
fi

# 5. Применяем.
nft -f "$RULES"
log "правила применены; открыты tcp: ${PUB_TCP_LIST}, ssh ${RUVDS_SSH_PORT:-22}"
warn "У вас ${CONFIRM_SEC}с. Проверьте доступ и выполните:  $0 --confirm"
warn "Если связь пропадёт — правила откатятся сами."
