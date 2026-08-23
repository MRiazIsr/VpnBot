#!/usr/bin/env bash
# Перевод боевых портов RuVDS со старого DNAT на relay — с автооткатом.
#
# Relay уже слушает свои offset-порты (порт+30000) и остаётся на них навсегда
# — этот скрипт его конфиг не трогает, он трогает только nft. Здесь мы:
#   1. снимаем снимок текущего DNAT (чтобы было чем откатываться);
#   2. sing-box check — конфиг relay не регенерируется, но убедиться, что то,
#      что уже на диске, валидно, до переключения на него боевого трафика — не лишнее;
#   3. ставим таймер автоотката;
#   4. заменяем DNAT на боевых портах правилом redirect на relay-порт;
#   5. ждём подтверждения оператора.
#
# Без `--confirm` в течение CONFIRM_SEC всё вернётся к DNAT автоматически.
#
#   ./promote.sh /etc/backhaul/params.env
#   ./promote.sh --confirm
#   ./promote.sh --rollback
set -euo pipefail

CONFIRM_SEC="${CONFIRM_SEC:-300}"
DNAT_SAVE=/etc/backhaul/relay-nat.nft
MARK=/etc/backhaul/.promote-pending
ONLY_PORT=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

do_rollback() {
  log "откат: возвращаем DNAT"
  # Сначала и безусловно — трафик. Конфиги и проверки потом: сломанный
  # конфиг не должен мешать вернуть пользователей на рабочий путь.
  if [[ -s "$DNAT_SAVE" ]]; then
    nft delete table ip relay 2>/dev/null || true
    if nft -f "$DNAT_SAVE"; then
      log "DNAT восстановлен из ${DNAT_SAVE}"
    else
      warn "nft -f не отработал — жму АВАРИЙНЫЙ РЫЧАГ: systemctl restart nftables"
      # Таймер автоотката дёргает этот путь без оператора: если рычаг не
      # нажать автоматически, прод останется вовсе без DNAT и без кричащего
      # предупреждения — молча. nftables.service = ExecStop=nft flush ruleset
      # + ExecStart=nft -f /etc/nftables.conf, а там все три боевых таблицы
      # (ip relay, ip mss_clamp, inet mtproxy_smart_syn_alt).
      if systemctl restart nftables; then
        log "АВАРИЙНЫЙ РЫЧАГ сработал: nftables перезапущен из /etc/nftables.conf"
      else
        warn "АВАРИЙНЫЙ РЫЧАГ ТОЖЕ НЕ СРАБОТАЛ — прод без DNAT, нужен человек немедленно"
      fi
    fi
  else
    warn "снимок ${DNAT_SAVE} пуст или отсутствует"
    warn "АВАРИЙНЫЙ РЫЧАГ: systemctl restart nftables"
  fi

  # Конфиг relay (/etc/vpnbot/backhaul.json, /etc/sing-box-relay/config.json)
  # promote.sh не меняет: offset-порты штатные, а не временный staging перед
  # переездом на боевые — регенерации в боевом режиме больше нет (см. выше).
  # Значит relay и backhaul-monitor всё это время работали без остановки, и
  # восстанавливать/перезапускать их при откате нечего и незачем — а
  # перезапуск здесь был бы прямо вреден: откат это момент, когда мы
  # возвращаем сервис, а не тревожим его. Всё, что нужно откату — уже сделано
  # выше: nft-таблица вернулась к DNAT.
  rm -f "$MARK"
  log "откат завершён: трафик снова идёт через DNAT"
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
  --only)
    [[ -n "${2:-}" ]] || { echo "--only требует номер порта" >&2; exit 1; }
    ONLY_PORT="$2"
    shift 2
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
# nft, а не iptables: боевой DNAT живёт в table ip relay, а iptables на этой
# машине собран как legacy и nftables-правил не видит вовсе. Снимок через
# iptables-save оказывался пустым, и откат был no-op.
nft list table ip relay > "$DNAT_SAVE"
chmod 600 "$DNAT_SAVE"
# Пустой снимок — это отсутствие страховки. Дальше идти нельзя.
if ! grep -qE 'dnat|redirect' "$DNAT_SAVE"; then
  echo "ОТКАЗ: снимок ${DNAT_SAVE} не содержит правил dnat/redirect." >&2
  echo "  Без рабочего снимка откат невозможен. Проверьте: nft list table ip relay" >&2
  exit 1
fi
log "в снимке правил: $(grep -cE 'dnat|redirect' "$DNAT_SAVE")"

# Конфиг relay не регенерируется: offset-порты (порт+30000) штатные с
# установки и promote.sh их не трогает — переводит только nft. Проверка
# ниже — предполётная: убедиться, что то, что уже лежит на диске, валидно,
# прежде чем направить на него боевой трафик, а не подтверждение регенерации.
log "sing-box check (конфиг relay не менялся)"
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

# Таймер — единственная страховка на случай, если оператор потеряет связь с
# машиной. Не взведён — прод не трогаем.
if ! systemctl is-active --quiet backhaul-promote-rollback.timer; then
  echo "ОТКАЗ: таймер автоотката не взведён." >&2
  echo "  Проверьте: systemctl status backhaul-promote-rollback.timer" >&2
  exit 1
fi
log "таймер автоотката взведён на ${CONFIRM_SEC}с"

log "переводим боевые порты с DNAT на relay (redirect)"

# Канарейка: по умолчанию переводим всё, но с --only <порт> двигаем ровно один.
# Relay уже поднят и слушает свои offset-порты (порт+30000) — перевод порта
# это редирект боевого порта на него, поэтому проверить схему можно на одном
# профиле, не подвергая риску остальные.
if [[ -n "$ONLY_PORT" ]]; then
  PROMOTE_PORTS="$ONLY_PORT"
  found=false
  for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
    [[ "$p" == "$ONLY_PORT" ]] && found=true
  done
  [[ "$found" == true ]] || {
    echo "ОТКАЗ: порт $ONLY_PORT не входит в RELAY_*_PORTS" >&2; exit 1; }
  log "канареечный перевод: только порт $ONLY_PORT"
else
  PROMOTE_PORTS="${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}"
  log "перевод всех портов: $PROMOTE_PORTS"
fi

for p in ${PROMOTE_PORTS}; do
  # Список снимаем отдельной командой и проверяем ЕЁ код возврата: раньше это
  # было nft ... | awk ..., а под pipefail код пайпа — это код awk, который на
  # пустом вводе всегда 0. Сломанный nft (права, race, бинарь недоступен) тем
  # самым выглядел неотличимо от "правило уже снято" на каждом порту цикла.
  if ! listing=$(nft -a list table ip relay 2>&1); then
    echo "ОТКАЗ: nft -a list table ip relay не отработал — не знаем, в каком" >&2
    echo "  состоянии прод. Продолжать цикл вслепую нельзя, откатываюсь." >&2
    echo "  Вывод nft: $listing" >&2
    do_rollback
    exit 1
  fi
  handle=$(awk -v p="$p" '$0 ~ "dport " p " (dnat|redirect)" {print $NF; exit}' <<< "$listing")
  if [[ -z "$handle" ]]; then
    warn "порт $p: правило dnat не найдено — возможно, уже переведён"
    continue
  fi

  # ЗАМЕНА правила, а не удаление. Relay слушает порт+30000 постоянно, а на
  # самом боевом порту всё это время сидит основной sing-box RuVDS (pid 702,
  # route.final=direct). Удалить dnat и не завести взамен ничего — значит на
  # промежутке между командами скормить новые соединения основному инстансу:
  # клиент подключится, всё будет работать, но выход окажется русским вместо
  # немецкого — и это не видно ни в логах relay (он не получает соединений),
  # ни в мониторинге (порт как ни в чём не бывало отвечает).
  #
  # Делаем это ОДНОЙ командой `nft replace rule ... handle H ...`, а не
  # `delete` + `add` по отдельности. `nft replace` меняет правило по тому же
  # handle одной netlink-транзакцией (единый батч между BATCH_BEGIN/END,
  # коммит в ядре атомарен через generation ID) — датаплейн видит либо
  # старое правило (dnat), либо новое (redirect), состояния "нет ни того, ни
  # другого" не существует в принципе. Два отдельных вызова nft — это два
  # отдельных батча, а значит измеримый интервал между ними, где боевой порт
  # временно достаётся основному sing-box. `replace` не сокращает этот
  # интервал, а устраняет его, поэтому отдельно вымерять окно (как
  # предполагалось изначально) не требуется.
  relay_port=$(( p + STAGING_PORT_OFFSET ))
  if ! nft replace rule ip relay prerouting handle "$handle" \
      tcp dport "$p" redirect to :"$relay_port"; then
    warn "порт $p: не удалось заменить dnat на redirect :$relay_port — откатываюсь"
    do_rollback
    exit 1
  fi
  log "порт $p переведён на relay :$relay_port (redirect, handle $handle)"
done

# Ни sing-box-relay, ни backhaul-monitor не перезапускаются: их конфиги не
# менялись (offset-порты штатные, backhaul.json promote.sh не трогает) —
# сменились только nft-правила, а это состояние ядра, никак не связанное с
# тем, что уже слушает relay. Перезапуск тут ничего не чинит, зато рвёт все
# соединения, которые уже идут через relay. Статус ниже — просто наблюдение.
log "проверка: relay уже слушает свои порты, рестарт не требуется"
systemctl --no-pager --lines=5 status sing-box-relay || true

warn "У вас ${CONFIRM_SEC}с на проверку живым клиентом."
warn "Всё хорошо →  $0 --confirm"
warn "Что-то не так → $0 --rollback   (или просто ничего не делайте: откатится само)"
