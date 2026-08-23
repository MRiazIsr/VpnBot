#!/usr/bin/env bash
# Перевод боевых портов RuVDS со старого DNAT на relay — с автооткатом.
#
# Relay уже слушает свои offset-порты (порт+30000) и остаётся на них навсегда
# — этот скрипт его конфиг не трогает, он трогает только nft. Здесь мы:
#   1. снимаем снимок текущего DNAT (чтобы было чем откатываться);
#   2. sing-box check — конфиг relay не регенерируется, но убедиться, что то,
#      что уже на диске, валидно, до переключения на него боевого трафика — не лишнее;
#   3. проверяем, что relay жив и реально слушает свои порты;
#   4. ставим таймер автоотката;
#   5. заменяем DNAT на боевых портах правилом redirect на relay-порт;
#   6. ждём подтверждения оператора.
#
# Без `--confirm` в течение CONFIRM_SEC всё вернётся к DNAT автоматически.
#
#   ./promote.sh /etc/backhaul/params.env
#   ./promote.sh /etc/backhaul/params.env --only 2058
#   ./promote.sh --confirm
#   ./promote.sh --rollback
#
# Этот скрипт НИКОГДА не пишет в /etc/nftables.conf: оттуда работает аварийный
# рычаг `systemctl restart nftables`, возвращающий боевое состояние целиком.
set -euo pipefail

CONFIRM_SEC="${CONFIRM_SEC:-300}"
DNAT_SAVE=/etc/backhaul/relay-nat.nft
MARK=/etc/backhaul/.promote-pending
TIMER=backhaul-promote-rollback.timer
ONLY_PORT=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

usage() { sed -n '2,22p' "$0" | sed 's/^# \?//'; }

# Возвращает 0 только если прод действительно вернулся на DNAT. Ненулевой код
# обязателен: do_rollback вызывается из systemd-таймера, и «успех» при
# невосстановленном проде означал бы, что юнит зелёный, а пользователи в это
# время идут мимо DNAT — молча и с русским выходом.
do_rollback() {
  local rc=0 restore=""
  log "откат: возвращаем DNAT"

  # Таймер снимаем в самом начале. Иначе после ручного `--rollback` он всё
  # равно сработает позже и повторит откат поверх того, что оператор успел
  # сделать за это время.
  systemctl stop "$TIMER" >/dev/null 2>&1 || true
  systemctl disable "$TIMER" >/dev/null 2>&1 || true

  # Сначала и безусловно — трафик. Конфиги и проверки потом: сломанный
  # конфиг не должен мешать вернуть пользователей на рабочий путь.
  if [[ -s "$DNAT_SAVE" ]]; then
    # Одной транзакцией, а не `nft delete` + `nft -f`: два отдельных вызова —
    # это два батча и измеримый промежуток, в котором таблицы ip relay нет
    # вовсе, а значит новые соединения на боевых портах достаются основному
    # sing-box RuVDS и выходят в интернет с русского адреса. Идиома
    # add+delete+определение штатная: add нужен, чтобы delete не уронил
    # транзакцию, если таблицы почему-то нет.
    # Путь фиксированный, а не mktemp: откат обязан работать и когда /tmp
    # переполнен или смонтирован только на чтение.
    restore="${DNAT_SAVE}.restore"
    { printf 'add table ip relay\ndelete table ip relay\n'; cat "$DNAT_SAVE"; } > "$restore"
    if nft -f "$restore"; then
      log "DNAT восстановлен из ${DNAT_SAVE} (одной транзакцией)"
    else
      warn "nft -f не отработал — жму АВАРИЙНЫЙ РЫЧАГ: systemctl restart nftables"
      # Таймер автоотката дёргает этот путь без оператора: если рычаг не
      # нажать автоматически, прод останется вовсе без DNAT и без кричащего
      # предупреждения — молча. nftables.service = ExecStop=nft flush ruleset
      # + ExecStart=nft -f /etc/nftables.conf, а там все три боевых таблицы
      # (ip relay, ip mss_clamp, inet mtproxy_smart_syn_alt).
      if pull_emergency_lever; then
        log "АВАРИЙНЫЙ РЫЧАГ сработал: nftables перезапущен из /etc/nftables.conf"
      else
        warn "АВАРИЙНЫЙ РЫЧАГ ТОЖЕ НЕ СРАБОТАЛ — прод без DNAT, нужен человек немедленно"
        rc=1
      fi
    fi
    rm -f "$restore"
  else
    # Снимка нет или он пуст — например, предыдущий запуск оборвался на `nft
    # list` или файл потеряли. Печатать подсказку про рычаг и возвращать успех
    # нельзя: этот путь достижим из таймера, у которого оператора нет, и
    # «успешный» откат оставил бы порты на relay навсегда.
    warn "снимок ${DNAT_SAVE} пуст или отсутствует — жму АВАРИЙНЫЙ РЫЧАГ"
    if pull_emergency_lever; then
      log "АВАРИЙНЫЙ РЫЧАГ сработал: nftables перезапущен из /etc/nftables.conf"
    else
      warn "АВАРИЙНЫЙ РЫЧАГ НЕ СРАБОТАЛ — прод без DNAT, нужен человек немедленно"
      rc=1
    fi
  fi

  # Конфиг relay (/etc/vpnbot/backhaul.json, /etc/sing-box-relay/config.json)
  # promote.sh не меняет: offset-порты штатные, а не временный staging перед
  # переездом на боевые — регенерации в боевом режиме больше нет (см. выше).
  # Значит relay и backhaul-monitor всё это время работали без остановки, и
  # восстанавливать/перезапускать их при откате нечего и незачем — а
  # перезапуск здесь был бы прямо вреден: откат это момент, когда мы
  # возвращаем сервис, а не тревожим его. Всё, что нужно откату — уже сделано
  # выше: nft-таблица вернулась к DNAT.
  if [[ "$rc" -eq 0 ]]; then
    rm -f "$MARK"
    log "откат завершён: трафик снова идёт через DNAT"
  else
    # Метку НЕ снимаем: она сигнализирует остальным скриптам (nftables-apply.sh
    # отказывается работать при неподтверждённом переводе), что состояние
    # прода не приведено в порядок.
    warn "ОТКАТ НЕ ЗАВЕРШЁН. Ручные шаги, по порядку:"
    warn "  1) systemctl restart nftables"
    warn "  2) nft list table ip relay | grep -cE 'dnat|redirect'   # должно быть 11"
    warn "  3) убрать ${MARK} только после того, как правила на месте"
  fi
  return "$rc"
}

# Аварийный рычаг: nftables.service = ExecStop=nft flush ruleset +
# ExecStart=nft -f /etc/nftables.conf. Возвращает боевое состояние целиком.
# После перезапуска ПРОВЕРЯЕМ результат: systemctl может отчитаться успехом,
# а таблица прод-правил при этом не появиться (например, файл затёрли).
pull_emergency_lever() {
  systemctl restart nftables || return 1
  local n
  n=$(nft list table ip relay 2>/dev/null | grep -cE 'dnat|redirect' || true)
  if [[ "${n:-0}" -eq 0 ]]; then
    warn "systemctl restart nftables прошёл, но в table ip relay нет правил dnat/redirect"
    warn "  значит /etc/nftables.conf не содержит боевого DNAT — рычаг не помог"
    return 1
  fi
  log "после рычага в table ip relay правил dnat/redirect: ${n}"
  return 0
}

# ─────────────────────────── разбор аргументов ───────────────────────────
# Циклом, а не `case "$1"`. Раньше смотрелся только первый аргумент, и
# документированная форма `promote.sh params.env --only 2058` молча
# превращалась в перевод ВСЕХ портов: флаг просто выпадал из рассмотрения.
ACTION=""
PARAMS=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --confirm|--rollback)
      [[ -z "$ACTION" ]] || { echo "ОТКАЗ: ${ACTION} и ${1} взаимоисключающи" >&2; exit 1; }
      ACTION="$1"; shift ;;
    --only)
      [[ -n "${2:-}" ]] || { echo "--only требует номер порта" >&2; exit 1; }
      ONLY_PORT="$2"; shift 2 ;;
    --only=*)
      ONLY_PORT="${1#*=}"
      [[ -n "$ONLY_PORT" ]] || { echo "--only требует номер порта" >&2; exit 1; }
      shift ;;
    -h|--help)
      usage; exit 0 ;;
    -*)
      echo "ОТКАЗ: неизвестный флаг: $1" >&2
      echo "  Допустимо: --confirm, --rollback, --only <порт>, --help" >&2
      exit 1 ;;
    *)
      [[ -z "$PARAMS" ]] || { echo "ОТКАЗ: лишний аргумент: $1" >&2; exit 1; }
      PARAMS="$1"; shift ;;
  esac
done

if [[ -n "$ACTION" ]]; then
  [[ -z "$ONLY_PORT" ]] || { echo "ОТКАЗ: --only несовместим с ${ACTION}" >&2; exit 1; }
  [[ -z "$PARAMS" ]] || warn "файл параметров в режиме ${ACTION} не используется — игнорирую ${PARAMS}"
fi

case "$ACTION" in
  --confirm)
    systemctl stop "$TIMER" 2>/dev/null || true
    systemctl disable "$TIMER" 2>/dev/null || true
    rm -f "$MARK"
    # Формулировка важна: DNAT не «снят», а ЗАМЕНЁН на redirect. Правило на
    # боевом порту есть и сейчас, просто ведёт не в Hetzner, а на relay-порт.
    log "перевод подтверждён: боевые порты заведены на relay правилом redirect"
    log "снимок правил ДО последнего перевода: ${DNAT_SAVE}"
    warn "следующий запуск promote.sh перезапишет этот снимок текущим состоянием —"
    warn "то есть уже переведённым. Исходный DNAT после этого есть только в"
    warn "/etc/nftables.conf."
    warn "Полный возврат к бою: systemctl restart nftables (из /etc/nftables.conf)."
    exit 0
    ;;
  --rollback)
    if do_rollback; then exit 0; else exit 1; fi
    ;;
esac

PARAMS="${PARAMS:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

# Смещение — основа всей схемы: relay слушает порт+STAGING_PORT_OFFSET. Ноль
# или пусто означало бы redirect порта сам в себя.
if [[ ! "${STAGING_PORT_OFFSET:-0}" =~ ^[0-9]+$ ]] || [[ "${STAGING_PORT_OFFSET:-0}" -le 0 ]]; then
  echo "ОТКАЗ: STAGING_PORT_OFFSET не задан или не положителен в ${PARAMS}." >&2
  exit 1
fi

# ─────────── порты, которые нельзя переводить ни при каких условиях ───────────
# Те же две преграды, что и в render-config.sh, но здесь они важнее: боевой
# трафик правит именно этот скрипт, а render-config.sh можно и не запускать.
# RU-порты через relay стали бы немецкими (в них весь смысл русского выхода),
# а PROTECTED_DIRECT_PORTS вообще не должны зависеть от состояния RuVDS.
for forbidden in ${RU_DIRECT_EXIT_PORTS:-2059 2060 8446} ${PROTECTED_DIRECT_PORTS:-4443 8444 8447}; do
  for p in ${RELAY_VLESS_PORTS:-} ${RELAY_MTPROTO_PORTS:-}; do
    if [[ "$p" == "$forbidden" ]]; then
      echo "ОТКАЗ: порт $forbidden стоит в RELAY_*_PORTS — переводить его нельзя." >&2
      echo "  RU_DIRECT_EXIT_PORTS выходят в интернет с русского адреса," >&2
      echo "  PROTECTED_DIRECT_PORTS ходят напрямую в Hetzner мимо RuVDS." >&2
      echo "  Уберите $forbidden из RELAY_*_PORTS в ${PARAMS}." >&2
      exit 1
    fi
  done
done

# Не переводим прод, пока не убедились, что хотя бы один backhaul живой.
log "проверка backhaul'ов перед переводом"
if ! /usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -probe; then
  warn "не все backhaul'ы прошли проверку — см. вывод выше"
  read -r -p "Всё равно переводить прод? [y/N] " ans
  [[ "$ans" == "y" || "$ans" == "Y" ]] || exit 1
fi

# Снимок — это «состояние до перевода». Если предыдущий перевод ещё не
# подтверждён (метка на месте), то «до» — это то, что было ДО НЕГО, а не то,
# что мы видим сейчас: сейчас уже стоят его redirect'ы. Перезаписав снимок,
# второй запуск подряд превратил бы откат в возврат к переведённому состоянию,
# то есть в no-op с бодрым сообщением об успехе.
if [[ -e "$MARK" && -s "$DNAT_SAVE" ]] && grep -qE 'dnat|redirect' "$DNAT_SAVE"; then
  warn "предыдущий перевод не подтверждён (${MARK} на месте)"
  warn "снимок ${DNAT_SAVE} НЕ перезаписываю: в нём состояние до того перевода"
  log "в снимке правил: $(grep -cE 'dnat|redirect' "$DNAT_SAVE")"
else
  log "снимок текущих правил NAT → ${DNAT_SAVE}"
  # nft, а не iptables: боевой DNAT живёт в table ip relay, а iptables на этой
  # машине собран как legacy и nftables-правил не видит вовсе. Снимок через
  # iptables-save оказывался пустым, и откат был no-op.
  #
  # Пишем во ВРЕМЕННЫЙ файл, а не сразу в $DNAT_SAVE: `>` обрезает цель до
  # запуска команды, поэтому упавший nft оставлял на месте рабочего снимка ноль
  # байт. Этот запуск после такого корректно отказывался стартовать, но снимок
  # предыдущего, уже живого перевода был уничтожен — то есть страховки лишался
  # не тот, кто ошибся, а тот, кто в этот момент работал.
  # `2>&1 > файл`: stdout уходит в снимок, stderr — в переменную, чтобы в
  # отказе была настоящая причина, а не «что-то не получилось».
  if ! nft_err=$(nft list table ip relay 2>&1 > "${DNAT_SAVE}.new"); then
    rm -f "${DNAT_SAVE}.new"
    echo "ОТКАЗ: nft list table ip relay не отработал: ${nft_err}" >&2
    echo "  Существующий снимок ${DNAT_SAVE} не тронут." >&2
    exit 1
  fi
  # Пустой снимок — это отсутствие страховки. Дальше идти нельзя.
  if ! grep -qE 'dnat|redirect' "${DNAT_SAVE}.new"; then
    rm -f "${DNAT_SAVE}.new"
    echo "ОТКАЗ: снимок не содержит правил dnat/redirect." >&2
    echo "  Без рабочего снимка откат невозможен. Проверьте: nft list table ip relay" >&2
    echo "  Существующий снимок ${DNAT_SAVE} не тронут." >&2
    exit 1
  fi
  chmod 600 "${DNAT_SAVE}.new"
  mv -f "${DNAT_SAVE}.new" "$DNAT_SAVE"
  log "в снимке правил: $(grep -cE 'dnat|redirect' "$DNAT_SAVE")"
fi

RELAY_DIR="${SINGBOX_RELAY_DIR:-/etc/sing-box-relay}"
RELAY_DIR="${RELAY_DIR%/}"

# Конфиг relay не регенерируется: offset-порты (порт+30000) штатные с
# установки и promote.sh их не трогает — переводит только nft. Проверка
# ниже — предполётная: убедиться, что то, что уже лежит на диске, валидно,
# прежде чем направить на него боевой трафик, а не подтверждение регенерации.
log "sing-box check (конфиг relay не менялся)"
"${SINGBOX_RELAY_BIN:-/usr/local/bin/sing-box}" check -c "${RELAY_DIR}/config.json"

# Раньше живость relay гарантировалась косвенно: promote.sh перезапускал
# sing-box-relay и падал, если тот не поднялся. Рестарт убрали (он рвал живые
# соединения и ничего не чинил), а вместе с ним пропала и единственная
# проверка того, что relay вообще работает. `sing-box check` смотрит файл на
# диске, `-probe` монитора — плечи через SOCKS; ни один из них не наблюдает
# процесс relay. Без этой проверки перевод направил бы боевой порт в никуда.
log "проверка, что relay запущен"
if ! systemctl is-active --quiet sing-box-relay; then
  echo "ОТКАЗ: sing-box-relay не запущен — переводить на него порты нельзя." >&2
  echo "  Проверьте: systemctl status sing-box-relay; journalctl -u sing-box-relay -n 50" >&2
  exit 1
fi

# Таймер отката ставим ДО того, как трогать прод.
cat > /etc/systemd/system/backhaul-promote-rollback.service <<EOF
[Unit]
Description=Автооткат перевода боевых портов на relay

[Service]
Type=oneshot
# Метка снимается при --confirm и при успешном откате. Без проверки юнит
# откатывал бы уже подтверждённый перевод, если таймер кто-то запустил снова.
# 'exit 0' до exec: отсутствие метки — это штатное «откатывать нечего», а не
# отказ; зато код самого отката пробрасывается наружу, и провалившийся откат
# виден как failed.
ExecStart=/bin/sh -c 'test -f ${MARK} || exit 0; exec $(readlink -f "$0") --rollback'
EOF
cat > /etc/systemd/system/backhaul-promote-rollback.timer <<EOF
[Unit]
Description=Автооткат promote через ${CONFIRM_SEC}с без подтверждения

[Timer]
OnActiveSec=${CONFIRM_SEC}
AccuracySec=5s
# Иначе отработавший таймер остаётся active без точки срабатывания: следующий
# запуск promote.sh увидел бы «активен», доложил «взведён» — и не взвёл ничего.
RemainAfterElapse=no
Unit=backhaul-promote-rollback.service

[Install]
WantedBy=timers.target
EOF
touch "$MARK"
systemctl daemon-reload
# restart, а не start: start на активном таймере — no-op, и перевод унаследовал
# бы остаток чужого отсчёта вместо своих CONFIRM_SEC.
systemctl restart "$TIMER"

# Таймер — единственная страховка на случай, если оператор потеряет связь с
# машиной. Не взведён — прод не трогаем.
if ! systemctl is-active --quiet "$TIMER"; then
  rm -f "$MARK"
  echo "ОТКАЗ: таймер автоотката не взведён." >&2
  echo "  Проверьте: systemctl status ${TIMER}" >&2
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
  for p in ${RELAY_VLESS_PORTS:-} ${RELAY_MTPROTO_PORTS:-}; do
    [[ "$p" == "$ONLY_PORT" ]] && found=true
  done
  [[ "$found" == true ]] || {
    echo "ОТКАЗ: порт $ONLY_PORT не входит в RELAY_*_PORTS" >&2; exit 1; }
  log "канареечный перевод: только порт $ONLY_PORT"
else
  PROMOTE_PORTS="${RELAY_VLESS_PORTS:-} ${RELAY_MTPROTO_PORTS:-}"
  [[ -n "${PROMOTE_PORTS// /}" ]] || {
    echo "ОТКАЗ: RELAY_VLESS_PORTS и RELAY_MTPROTO_PORTS пусты — переводить нечего" >&2
    exit 1; }
  log "перевод всех портов: $PROMOTE_PORTS"
fi

for p in ${PROMOTE_PORTS}; do
  relay_port=$(( p + STAGING_PORT_OFFSET ))

  # Список снимаем отдельной командой и проверяем ЕЁ код возврата: раньше это
  # было nft ... | awk ..., а под pipefail код пайпа — это код awk, который на
  # пустом вводе всегда 0. Сломанный nft (права, race, бинарь недоступен) тем
  # самым выглядел неотличимо от "правило уже снято" на каждом порту цикла.
  if ! listing=$(nft -a list table ip relay 2>&1); then
    echo "ОТКАЗ: nft -a list table ip relay не отработал — не знаем, в каком" >&2
    echo "  состоянии прод. Продолжать цикл вслепую нельзя, откатываюсь." >&2
    echo "  Вывод nft: $listing" >&2
    do_rollback || true
    exit 1
  fi

  # Relay слушает offset-порт ПОСТОЯННО, но список портов берётся из
  # params.env, а реальные слушатели — из конфига, собранного при установке.
  # Разъехались (порт добавили в params и не пересобрали конфиг) — редирект
  # уйдёт на порт, где никого нет, и профиль будет отвечать RST до
  # срабатывания таймера. Поэтому смотрим на сокет, а не на файл.
  if [[ -z "$(ss -tlnH "sport = :${relay_port}" 2>/dev/null || true)" ]]; then
    echo "ОТКАЗ: на relay-порту :${relay_port} (для боевого ${p}) никто не слушает." >&2
    echo "  Перевод направил бы трафик в никуда. Вероятная причина: порт ${p}" >&2
    echo "  добавлен в RELAY_*_PORTS без пересборки конфига relay." >&2
    echo "  Почините: render-config.sh ${PARAMS} && systemctl restart sing-box-relay" >&2
    do_rollback || true
    exit 1
  fi

  # Уже переведён — идемпотентно пропускаем.
  if grep -qE "dport ${p} redirect to :${relay_port}([^0-9]|\$)" <<< "$listing"; then
    log "порт $p уже переведён на relay :${relay_port} — пропускаю"
    continue
  fi

  # Ищем правило ТОЛЬКО в prerouting: handle уникален в пределах таблицы, но
  # `nft replace ... prerouting handle H` с handle из другой цепочки — это
  # либо ошибка, либо правка не того правила.
  handle=$(awk -v p="$p" '
    /^[[:space:]]*chain /  { chain = $2 }
    chain == "prerouting" && $0 ~ "dport " p " (dnat|redirect)" { print $NF; exit }
  ' <<< "$listing")

  if [[ -z "$handle" ]]; then
    # НЕ «возможно, уже переведён»: случай «уже переведён» отработан выше.
    # Здесь на боевом порту нет ни dnat, ни redirect — а это ровно то
    # состояние, ради недопущения которого существует вся эта ветка: пакеты
    # проваливаются в основной sing-box RuVDS (pid 702, route.final=direct),
    # клиент подключается как ни в чём не бывало, а выход оказывается русским
    # вместо немецкого. Ни в логах relay, ни в мониторинге это не видно.
    #
    # Правило не заводим сами: promote.sh не может отличить «DNAT потеряли»
    # от «порт намеренно обслуживается локально», а redirect на локально
    # обслуживаемом порту — это тот же молчаливый увод трафика, только
    # устроенный нашими руками. Поэтому громкий отказ и откат.
    echo "АВАРИЯ: на боевом порту ${p} нет правила ни dnat, ни redirect." >&2
    echo "  Значит трафик этого порта уже сейчас проваливается в локальный" >&2
    echo "  sing-box RuVDS и выходит в интернет с РУССКОГО адреса, а клиент" >&2
    echo "  этого не замечает. Молча продолжать нельзя — откатываюсь." >&2
    echo "  Проверить: nft -a list table ip relay | grep ' ${p} '" >&2
    echo "  Вернуть боевое состояние целиком: systemctl restart nftables" >&2
    echo "  Если порт ${p} действительно должен идти на relay, правило заводится" >&2
    echo "  вручную и осознанно:" >&2
    echo "    nft add rule ip relay prerouting tcp dport ${p} redirect to :${relay_port}" >&2
    do_rollback || true
    exit 1
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
  if ! nft replace rule ip relay prerouting handle "$handle" \
      tcp dport "$p" redirect to :"$relay_port"; then
    warn "порт $p: не удалось заменить dnat на redirect :$relay_port — откатываюсь"
    do_rollback || true
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
