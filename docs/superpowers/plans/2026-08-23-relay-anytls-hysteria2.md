# Перевод плеча RuVDS→Hetzner на relay с ярусами AnyTLS и Hysteria2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Перевести трансграничное плечо RuVDS→Hetzner с ядерного DNAT на sing-box relay с двумя управляемыми транспортами (AnyTLS, Hysteria2), не потеряв ни одного пользователя и имея работающий откат на каждом шаге.

**Architecture:** На RuVDS поднимается `sing-box-relay` — `direct` inbound на публичных портах, отдающий сырой TCP в selector; каждый ярус представлен локальным SOCKS5-адаптером. Reality/ShadowTLS/MTProto по-прежнему терминируются только на Hetzner, RuVDS ключей не видит. Перевод идёт по одному порту, откат — правка nft ruleset без рестарта сервисов.

**Tech Stack:** bash + nftables + systemd на RuVDS и Hetzner; Go (`service/backhaul`, `cmd/backhaul-monitor`) для генерации конфигов и health-checker; sing-box 1.12/1.13-dev (rev `596291567f`, уже установлен).

**Spec:** `docs/superpowers/specs/2026-08-23-singbox-upgrade-anytls-relay-design.md`

## Global Constraints

- **Пересобирать sing-box не нужно.** Установленный бинарь принимает `anytls`, `anytls`+reality, `hysteria2`+salamander, `hysteria2`+port hopping. Проверено 23.08.2026.
- **Боевые адреса RuVDS:** `87.247.157.120` (рабочий), `176.113.80.243` (рабочий), `194.87.80.237` (входящий TCP заблокирован, UDP проходит). Всё SSH — только на `87.247.157.120`.
- **Hetzner:** `49.13.201.110`, ключ `~/.ssh/cloud-hetzner-v2`, пользователь `root`.
- **RuVDS SSH:** ключ `~/.ssh/russian-vps`, пользователь `root`. Порт 22.
- **`promote.sh` не имеет права писать в `/etc/nftables.conf`.** Это условие уровня 4 отката: перезагрузка должна возвращать DNAT.
- **Порт 443 на RuVDS занят nginx** (`ssl_preread` по SNI). Он не может попасть в `RELAY_VLESS_PORTS` ни при каких условиях.
- **Порты 2059 (RU), 2060 (RU-TCP), 8446 (RU-STLS)** обязаны выходить с русского адреса и через relay идти не должны.
- **Порт 8445 (DE) намеренно остаётся на DNAT** весь период миграции — это нетронутый запасной путь.
- **Порты 4443, 8444, 8447** (`PROTECTED_DIRECT_PORTS`) неприкосновенны, у них ссылка ведёт прямо в Hetzner.
- Комментарии в коде и в bash — по-русски, как в остальном репозитории.

## File Structure

| Файл | Ответственность | Задача |
|---|---|---|
| `deploy/ruvds/backhaul/promote.sh` | перевод боевых портов и откат | 1, 3 |
| `deploy/ruvds/backhaul/render-config.sh` | сборка конфигов + запретительные проверки | 2 |
| `deploy/backhaul/params.env.example` | параметры: адреса, списки портов, ярусы | 2, 6, 7 |
| `scripts/backhaul/rollback-drill.sh` | учебный прогон отката на служебной таблице | 1 |
| `deploy/hetzner/backhaul/install.sh` | wg1, probe, sshd для emergency | 4 |
| `deploy/ruvds/backhaul/install.sh` | relay, монитор, адаптеры ярусов | 4, 6, 7 |

---

## Task 1: Починить откат в promote.sh

Сейчас откат — no-op. `promote.sh:73` снимает снимок через `iptables-save -t nat`, а боевой DNAT живёт в nftables; `iptables` на RuVDS собран как legacy и nftables-правил не видит. Проверено на машине: `iptables-save -t nat` → 0 правил, `nft list table ip relay` → 10.

Правильный образец уже есть в этом же репозитории — `nftables-apply.sh:52` использует `nft list ruleset > "$SAVED"`.

**Files:**
- Modify: `deploy/ruvds/backhaul/promote.sh:29-41` (функция `do_rollback`), `:72-74` (снимок)
- Create: `scripts/backhaul/rollback-drill.sh`

**Interfaces:**
- Produces: `do_rollback()` восстанавливает `table ip relay` из `/etc/backhaul/relay-nat.nft`; `snapshot_dnat()` пишет этот файл и завершается ошибкой при пустом снимке.

- [ ] **Step 1: Написать учебный прогон механики отката**

Создать `scripts/backhaul/rollback-drill.sh`. Это не красный тест: он проверяет механику снимка и восстановления nft напрямую и должен пройти сразу. Смысл — доказать, что механизм, на который мы переводим откат, работает на этой машине. Работает на **служебной таблице** `ip bh_drill`, не касаясь боевых правил:

```bash
#!/usr/bin/env bash
# Учебный прогон отката: доказывает, что снимок и восстановление nft работают,
# НЕ ТРОГАЯ боевые правила. Работает на служебной таблице ip bh_drill.
#
# Запускать на RuVDS перед тем, как полагаться на откат promote.sh.
#
#   ./rollback-drill.sh
set -euo pipefail

SNAP=/tmp/bh_drill.nft
fail() { printf '\033[1;31mПРОВАЛ:\033[0m %s\n' "$*" >&2; exit 1; }
ok()   { printf '\033[1;32mОК:\033[0m %s\n' "$*"; }

cleanup() { nft delete table ip bh_drill 2>/dev/null || true; rm -f "$SNAP"; }
trap cleanup EXIT

nft delete table ip bh_drill 2>/dev/null || true
nft add table ip bh_drill
nft add chain ip bh_drill prerouting '{ type nat hook prerouting priority dstnat; policy accept; }'
nft add rule ip bh_drill prerouting tcp dport 65531 dnat to 127.0.0.1:65532
nft add rule ip bh_drill prerouting tcp dport 65533 dnat to 127.0.0.1:65534

before=$(nft list table ip bh_drill | grep -c dnat)
[[ "$before" -eq 2 ]] || fail "не удалось создать служебные правила"

nft list table ip bh_drill > "$SNAP"
[[ -s "$SNAP" ]] || fail "снимок пустой"
grep -q dnat "$SNAP" || fail "в снимке нет правил dnat"
ok "снимок снят, правил: $before"

nft delete table ip bh_drill
nft list table ip bh_drill >/dev/null 2>&1 && fail "таблица не удалилась"
ok "таблица удалена (имитация promote)"

nft -f "$SNAP"
after=$(nft list table ip bh_drill | grep -c dnat)
[[ "$after" -eq "$before" ]] || fail "восстановлено $after правил вместо $before"
ok "восстановлено правил: $after — откат работает"

# Тот же прогон старым способом: должен показать, почему он не работал.
if iptables-save -t nat 2>/dev/null | grep -q 65531; then
  fail "iptables-save неожиданно видит nft-правила — пересмотреть вывод задачи"
fi
ok "iptables-save правил не видит — подтверждение исходного дефекта"
```

- [ ] **Step 2: Прогнать на RuVDS и убедиться, что механизм рабочий**

```bash
scp -i ~/.ssh/russian-vps scripts/backhaul/rollback-drill.sh root@87.247.157.120:/tmp/
ssh -i ~/.ssh/russian-vps root@87.247.157.120 'chmod +x /tmp/rollback-drill.sh && /tmp/rollback-drill.sh'
```

Ожидается пять строк `ОК:`, последняя — «iptables-save правил не видит — подтверждение исходного дефекта». Если последняя строка провалилась, значит на машине уже не legacy-iptables, и задачу надо пересмотреть.

- [ ] **Step 3: Заменить снимок в promote.sh**

Заменить блок `promote.sh:72-74`:

```bash
log "снимок текущих правил NAT → ${DNAT_SAVE}"
iptables-save -t nat > "$DNAT_SAVE"
chmod 600 "$DNAT_SAVE"
```

на:

```bash
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
```

Изменить путь снимка в шапке (`promote.sh:22`), чтобы имя не врало про формат:

```bash
DNAT_SAVE=/etc/backhaul/relay-nat.nft
```

- [ ] **Step 4: Переписать do_rollback**

Заменить `promote.sh:29-41` целиком:

```bash
do_rollback() {
  log "откат: возвращаем DNAT"
  # Сначала и безусловно — трафик. Конфиги и проверки потом: сломанный
  # конфиг не должен мешать вернуть пользователей на рабочий путь.
  if [[ -s "$DNAT_SAVE" ]]; then
    nft delete table ip relay 2>/dev/null || true
    if nft -f "$DNAT_SAVE"; then
      log "DNAT восстановлен из ${DNAT_SAVE}"
    else
      warn "nft -f не отработал — АВАРИЙНЫЙ РЫЧАГ: systemctl restart nftables"
    fi
  else
    warn "снимок ${DNAT_SAVE} пуст или отсутствует"
    warn "АВАРИЙНЫЙ РЫЧАГ: systemctl restart nftables"
  fi

  # Только после того, как трафик вернулся, приводим relay в staging-вид.
  if [[ -s "$CFG_SAVE" ]]; then
    cp "$CFG_SAVE" /etc/vpnbot/backhaul.json
    /usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -render-relay \
      > /etc/sing-box-relay/config.json || warn "рендер staging-конфига не удался"
    "${SINGBOX_RELAY_BIN:-/usr/local/bin/sing-box}" check -c /etc/sing-box-relay/config.json \
      && systemctl restart sing-box-relay backhaul-monitor \
      || warn "конфиг relay не прошёл проверку — relay оставлен как есть"
  fi
  rm -f "$MARK"
  log "откат завершён: трафик снова идёт через DNAT"
}
```

- [ ] **Step 5: Добавить проверку взведённого таймера перед изменением прода**

Вставить в `promote.sh` сразу после `systemctl start backhaul-promote-rollback.timer`:

```bash
# Таймер — единственная страховка на случай, если оператор потеряет связь с
# машиной. Не взведён — прод не трогаем.
if ! systemctl is-active --quiet backhaul-promote-rollback.timer; then
  echo "ОТКАЗ: таймер автоотката не взведён." >&2
  echo "  Проверьте: systemctl status backhaul-promote-rollback.timer" >&2
  exit 1
fi
log "таймер автоотката взведён на ${CONFIRM_SEC}с"
```

- [ ] **Step 6: Снизить окно подтверждения по умолчанию**

`promote.sh:21`: заменить `CONFIRM_SEC="${CONFIRM_SEC:-900}"` на `CONFIRM_SEC="${CONFIRM_SEC:-300}"`.

- [ ] **Step 7: Проверить синтаксис**

```bash
bash -n deploy/ruvds/backhaul/promote.sh && echo "синтаксис ок"
shellcheck deploy/ruvds/backhaul/promote.sh || true
```

Ожидается «синтаксис ок». Замечания shellcheck просмотреть, критичные исправить.

- [ ] **Step 8: Коммит**

```bash
git add deploy/ruvds/backhaul/promote.sh scripts/backhaul/rollback-drill.sh
git commit -m "fix(backhaul): откат promote.sh был no-op — снимок через nft вместо iptables

iptables на RuVDS собран как legacy и правил nftables не видит: iptables-save
-t nat даёт 0 правил, при том что nft list table ip relay даёт 10. Снимок
получался пустым, а do_rollback() восстанавливал пустоту. Правила при этом
удалялись корректно через nft delete — дверь в одну сторону.

Снимок и восстановление переведены на nft, добавлена проверка непустоты
снимка (одна она поймала бы дефект), восстановление DNAT идёт первым и
безусловно, добавлена проверка что таймер автоотката реально взведён.
Окно подтверждения снижено 900с -> 300с.

scripts/backhaul/rollback-drill.sh доказывает работоспособность механизма на
служебной таблице, не касаясь боевых правил."
```

---

## Task 2: Запретительные проверки и правка params

Три ошибки в `params.env.example`, каждая ломает прод по-своему.

**443 в `RELAY_VLESS_PORTS`** — порт держит nginx (`ssl_preread` по SNI: `lk.rt.ru` → туннель, `cdn.moskva.live` → альт-туннель, прочее → сайт-приманка). sing-box не сможет забиндить его и **не поднимется целиком**, то есть один лишний порт кладёт все relay-порты сразу.

**2059, 2060, 8446** — это RU, RU-TCP и RU-STLS. Они существуют, чтобы выходить с русского адреса; relay уведёт их в Hetzner, и профили потеряют смысл.

**`RUVDS_IP=194.87.80.237`** — адрес, у которого входящий TCP заблокирован. Все скрипты, читающие params, будут ходить в таймаут.

**Files:**
- Modify: `deploy/backhaul/params.env.example`
- Modify: `deploy/ruvds/backhaul/render-config.sh:19-35` (расширение блока проверок)

**Interfaces:**
- Consumes: `RELAY_VLESS_PORTS`, `RELAY_MTPROTO_PORTS`, `PROTECTED_DIRECT_PORTS` из params.
- Produces: `RU_DIRECT_EXIT_PORTS` — новая переменная params, список портов с русским выходом.

- [ ] **Step 1: Написать проверку, которая падает на текущем params**

Добавить в `render-config.sh` сразу после блока `PROTECTED_DIRECT_PORTS` (после строки 35):

```bash
# ─────────────────── порты с русским выходом ───────────────────
# RU (2059), RU-TCP (2060), RU-STLS (8446) обслуживаются локальным sing-box на
# RuVDS и выходят в интернет с русского адреса — в этом весь их смысл. Relay
# отправляет всё на Hetzner, то есть превратил бы их в немецкие.
RU_DIRECT_EXIT_PORTS="${RU_DIRECT_EXIT_PORTS:-2059 2060 8446}"
for ru in $RU_DIRECT_EXIT_PORTS; do
  for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
    if [[ "$p" == "$ru" ]]; then
      echo "ОТКАЗ: порт $ru попал в список relay." >&2
      echo "  Это профиль с выходом с РУССКОГО адреса; relay уведёт его в Hetzner." >&2
      echo "  Уберите $ru из RELAY_VLESS_PORTS в params.env." >&2
      exit 1
    fi
  done
done

# ─────────────────── порты, занятые чужим процессом ───────────────────
# sing-box не поднимется, если хоть один listen-порт занят. Отказ одного порта
# кладёт ВЕСЬ relay, поэтому проверяем до генерации, а не после рестарта.
# Пример: 443 держит nginx (ssl_preread по SNI).
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
  listen_port="$p"
  [[ "$MODE" == "staging" ]] && listen_port=$(( p + STAGING_PORT_OFFSET ))
  holder=$(ss -tlnp "sport = :${listen_port}" 2>/dev/null \
    | awk 'NR>1 {print $NF}' | grep -v '^sing-box' | head -1 || true)
  if [[ -n "$holder" ]]; then
    echo "ОТКАЗ: порт $listen_port уже занят: $holder" >&2
    echo "  sing-box не забиндит его и не поднимется — упадут ВСЕ relay-порты." >&2
    echo "  Уберите $p из RELAY_*_PORTS либо освободите порт." >&2
    exit 1
  fi
done
```

- [ ] **Step 2: Прогнать проверку на RuVDS с заведомо плохим params — должна отказать дважды**

Проверяем обе новые преграды на отдельном файле параметров, не трогая боевой:

```bash
scp -i ~/.ssh/russian-vps deploy/ruvds/backhaul/render-config.sh \
    deploy/backhaul/params.env.example root@87.247.157.120:/tmp/

# (а) порт с русским выходом
ssh -i ~/.ssh/russian-vps root@87.247.157.120 'set -e
  sed "s|^RELAY_VLESS_PORTS=.*|RELAY_VLESS_PORTS=\"2053 2059\"|" /tmp/params.env.example > /tmp/bad-ru.env
  MODE=production bash /tmp/render-config.sh /tmp/bad-ru.env 2>&1 | head -3 || true'

# (б) порт, занятый nginx
ssh -i ~/.ssh/russian-vps root@87.247.157.120 'set -e
  sed "s|^RELAY_VLESS_PORTS=.*|RELAY_VLESS_PORTS=\"443 2053\"|" /tmp/params.env.example > /tmp/bad-443.env
  MODE=production bash /tmp/render-config.sh /tmp/bad-443.env 2>&1 | head -3 || true'
```

Ожидается: (а) «ОТКАЗ: порт 2059 попал в список relay» с пояснением про русский адрес; (б) «ОТКАЗ: порт 443 уже занят» с упоминанием nginx. Если хоть одна проверка не сработала — исправить, прежде чем идти дальше. Убрать за собой: `rm -f /tmp/bad-*.env /tmp/render-config.sh /tmp/params.env.example`.

- [ ] **Step 3: Исправить params.env.example**

Заменить `RUVDS_IP=194.87.80.237` на:

```bash
# Рабочий адрес. У машины три адреса на eth0; на 194.87.80.237 входящий TCP
# заблокирован вне хоста (замер 23.08.2026: :22 и :443 в таймаут, при этом
# UDP на тот же адрес доходит). Административный доступ — только сюда.
RUVDS_IP=87.247.157.120
```

Заменить блок `RELAY_VLESS_PORTS`:

```bash
# Публичные порты RuVDS, которые relay обслуживает.
#
# НЕТ 443: его держит nginx (ssl_preread по SNI). sing-box не забиндит занятый
# порт и не поднимется ЦЕЛИКОМ — один лишний порт в списке кладёт весь relay.
#
# НЕТ 2059/2060/8446 (RU, RU-TCP, RU-STLS): они выходят в интернет с русского
# адреса через локальный sing-box, а relay увёл бы их в Hetzner.
#
# НЕТ 8445 (DE): намеренно оставлен на DNAT весь период миграции как
# нетронутый запасной путь — по тому же принципу, по которому в аварию
# 02.08.2026 выжили профили, не зависевшие от общей схемы.
#
# НЕТ 4443/8444/8447: см. PROTECTED_DIRECT_PORTS.
RELAY_VLESS_PORTS="2053 2054 2055 2056 2057 2058"
RELAY_MTPROTO_PORTS="9443"

# Порты с русским выходом. render-config.sh откажется собирать конфиг, если
# любой из них попадёт в RELAY_*_PORTS.
RU_DIRECT_EXIT_PORTS="2059 2060 8446"
```

- [ ] **Step 4: Проверить синтаксис**

```bash
bash -n deploy/ruvds/backhaul/render-config.sh && echo "синтаксис ок"
```

- [ ] **Step 5: Коммит**

```bash
git add deploy/backhaul/params.env.example deploy/ruvds/backhaul/render-config.sh
git commit -m "fix(backhaul): 443 и RU-профили в списке relay ломали прод

443 держит nginx (ssl_preread по SNI). sing-box не биндит занятый порт и не
поднимается целиком — один лишний порт в списке уронил бы ВСЕ relay-порты.

2059/2060/8446 (RU, RU-TCP, RU-STLS) выходят с русского адреса через
локальный sing-box; relay увёл бы их в Hetzner и обессмыслил.

RUVDS_IP указывал на 194.87.80.237, где входящий TCP заблокирован.

Добавлены две запретительные проверки в render-config.sh по образцу
существующей PROTECTED_DIRECT_PORTS: порты с русским выходом и порты,
занятые чужим процессом. 8445 (DE) намеренно оставлен на DNAT как
нетронутый запасной путь."
```

---

## Task 3: Канареечный перевод одного порта

`promote.sh` переводит все порты одним циклом (`:107-119`). Relay уже слушает, поэтому схему можно проверить на одном порту и только потом двигать остальные.

**Files:**
- Modify: `deploy/ruvds/backhaul/promote.sh` (разбор аргументов и цикл удаления DNAT)

**Interfaces:**
- Consumes: `do_rollback()` из задачи 1.
- Produces: `promote.sh --only <порт>` переводит ровно один порт; `PROMOTE_PORTS` — вычисленный список.

- [ ] **Step 1: Добавить разбор `--only`**

В блок `case "${1:-}" in` (`promote.sh:44`) добавить ветку перед `esac`:

```bash
  --only)
    [[ -n "${2:-}" ]] || { echo "--only требует номер порта" >&2; exit 1; }
    ONLY_PORT="$2"
    shift 2
    ;;
```

И объявить рядом с другими переменными в шапке:

```bash
ONLY_PORT=""
```

- [ ] **Step 2: Вычислять список переводимых портов**

Заменить строку цикла `for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do` (`promote.sh:109`) на:

```bash
# Канарейка: по умолчанию переводим всё, но с --only <порт> двигаем ровно один.
# Relay уже слушает на боевых портах, поэтому проверить схему можно на одном
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
```

- [ ] **Step 3: Убрать мёртвые вызовы iptables из цикла**

В том же цикле удалить две строки, которые ничего не делают (правил в iptables нет — проверено):

```bash
  iptables -t nat -D PREROUTING -p tcp --dport "$p" -j DNAT \
    --to-destination "${HETZNER_TARGET}:${p}" 2>/dev/null || true
  iptables -t nat -D POSTROUTING -d "${HETZNER_TARGET}" -p tcp --dport "$p" \
    -j MASQUERADE 2>/dev/null || true
```

Оставшееся удаление правила nft заменить на явное, с проверкой результата:

```bash
  handle=$(nft -a list table ip relay 2>/dev/null \
    | awk -v p="$p" '$0 ~ "dport " p " (dnat|redirect)" {print $NF; exit}')
  if [[ -z "$handle" ]]; then
    warn "порт $p: правило dnat не найдено — возможно, уже переведён"
    continue
  fi
  nft delete rule ip relay prerouting handle "$handle" \
    || { warn "не удалось удалить правило для $p — откатываюсь"; do_rollback; exit 1; }
  log "порт $p снят с DNAT (handle $handle)"
```

- [ ] **Step 4: Проверить синтаксис и логику разбора аргументов**

```bash
bash -n deploy/ruvds/backhaul/promote.sh && echo "синтаксис ок"
```

- [ ] **Step 5: Коммит**

```bash
git add deploy/ruvds/backhaul/promote.sh
git commit -m "feat(backhaul): канареечный перевод одного порта (--only)

promote.sh переводил все десять портов одним циклом. Relay уже слушает на
боевых портах, поэтому схему можно проверить на одном профиле.

Заодно убраны вызовы iptables из цикла удаления: правил в iptables нет
(проверено, боевой DNAT целиком в nftables), они молча ничего не делали.
Удаление правила nft теперь явное: не нашли handle — предупреждаем и
пропускаем, не удалили — откатываемся сразу, а не продолжаем цикл."
```

---

## Task 4: Развернуть Hetzner-часть и relay на staging

**Files:**
- Использует существующие: `deploy/hetzner/backhaul/install.sh`, `deploy/ruvds/backhaul/install.sh`, `scripts/backhaul/verify.sh`

**Interfaces:**
- Consumes: `params.env` из задачи 2.
- Produces: работающие `sing-box-relay` (staging-порты 32053–32058, 39443), `backhaul-monitor`, `backhaul-fssh@vless`, `backhaul-fssh@mtproto` на RuVDS; `wg-quick@wg1`, `backhaul-probe` на Hetzner.

- [ ] **Step 1: Заполнить params.env**

```bash
cp deploy/backhaul/params.env.example deploy/backhaul/params.env
```

Заполнить: `CLASH_API_SECRET=$(openssl rand -hex 24)`. `FRP_TOKEN` и `BHWS_UUID` оставить пустыми — primary и secondary выключены. `PRIMARY_ENABLED=false`, `SECONDARY_ENABLED=false`.

- [ ] **Step 2: Развернуть Hetzner**

```bash
scp -i ~/.ssh/cloud-hetzner-v2 -r deploy/hetzner/backhaul deploy/backhaul/params.env \
  root@49.13.201.110:/tmp/
ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110 \
  'cd /tmp/backhaul && bash install.sh /tmp/params.env'
```

Проверить:

```bash
ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110 \
  'systemctl is-active wg-quick@wg1 backhaul-probe; ip -4 addr show wg1 | grep inet'
```

Ожидается `active`, `active` и адрес `10.9.0.1/24`.

- [ ] **Step 3: Развернуть RuVDS на staging-портах**

```bash
scp -i ~/.ssh/russian-vps -r deploy/ruvds/backhaul deploy/backhaul/params.env \
  root@87.247.157.120:/tmp/
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'cd /tmp/backhaul && MODE=staging bash install.sh /tmp/params.env'
```

- [ ] **Step 4: Убедиться, что прод не задет**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'echo "боевых правил DNAT:"; nft list table ip relay | grep -cE "dnat|redirect"; \
   echo "staging-порты:"; ss -tln | grep -cE ":3205[3-8]|:39443"'
```

Ожидается 11 боевых правил (как было) и 7 staging-слушателей. Если боевых стало меньше — немедленно `systemctl restart nftables` и разбираться.

- [ ] **Step 5: Прогнать сквозную проверку**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  '/usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -probe'
```

Ожидается: ярус `emergency` здоров по обоим классам, `primary` и `secondary` помечены disabled и не опрашиваются.

- [ ] **Step 6: Проверить живым клиентом на staging-порту**

Взять профиль DE-WL (2058), в клиенте поменять порт на **32058**, подключиться, открыть любой сайт. Это доказывает всю цепочку `клиент → relay → emergency → Hetzner` до того, как трогается прод.

- [ ] **Step 7: Зафиксировать состояние**

```bash
git add deploy/backhaul/params.env.example
git commit -m "chore(backhaul): staging развёрнут, emergency проверен живым клиентом" --allow-empty
```

---

## Task 5: Учебный откат и канареечный перевод 2058

Порядок здесь принципиален: **сначала доказать, что откат работает, потом на него полагаться.**

- [ ] **Step 1: Прогнать учебный откат на боевой машине**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 '/tmp/rollback-drill.sh'
```

Все строки `ОК`. Иначе — стоп, возврат к задаче 1.

- [ ] **Step 2: Снять эталонный отпечаток боевых правил**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'nft list table ip relay | grep -E "dnat|redirect" | sort > /root/relay-before.txt; \
   wc -l < /root/relay-before.txt'
```

Записать число — по нему проверяется полнота отката.

- [ ] **Step 3: Перевести один порт**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'cd /tmp/backhaul && CONFIRM_SEC=300 bash promote.sh --only 2058'
```

Ожидается: снимок непустой с числом правил из шага 2, таймер взведён, «порт 2058 снят с DNAT».

- [ ] **Step 4: Проверить живым клиентом на боевом порту**

Профиль DE-WL (2058) без изменений в клиенте. Подключиться, открыть сайт. Параллельно:

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'journalctl -u sing-box-relay --no-pager -n 20; \
   ss -tn state established "( dport = :2058 or sport = :2058 )" | head'
```

- [ ] **Step 5: Намеренно откатиться, не подтверждая**

Даже если всё работает — **первый прогон откатить вручную**, чтобы убедиться в обратимости:

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 'cd /tmp/backhaul && bash promote.sh --rollback'
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'nft list table ip relay | grep -E "dnat|redirect" | sort > /root/relay-after.txt; \
   diff /root/relay-before.txt /root/relay-after.txt && echo "ПРАВИЛА СОВПАЛИ"'
```

Ожидается `ПРАВИЛА СОВПАЛИ`. Это доказательство, ради которого делалась задача 1.

- [ ] **Step 6: Перевести 2058 повторно и подтвердить**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'cd /tmp/backhaul && CONFIRM_SEC=300 bash promote.sh --only 2058'
# проверить клиентом, затем:
ssh -i ~/.ssh/russian-vps root@87.247.157.120 'cd /tmp/backhaul && bash promote.sh --confirm'
```

- [ ] **Step 7: Наблюдать сутки, затем перевести остальные**

Через сутки без жалоб перевести остальные порты по одному тем же способом (`--only 2053`, затем 2054 и так далее). 8445 (DE) не трогать — он остаётся запасным путём.

---

## Task 6: Ярус AnyTLS

AnyTLS занимает слот `primary` (`Rank=1`). Слот свободен: он резервировался под израильский узел, которого нет.

**Переименовывать константы ярусов не будем** — вопреки предположению в спеке. `Rank` задаёт приоритет, имя — только идентификатор, а переименование инвалидирует ключи в `/var/lib/vpnbot/backhaul-state.json` и требует правок в мониторе, `switch.sh` и генераторе. Цена без функциональной отдачи. Вместо этого — комментарий в params, объясняющий, что имя историческое.

**Files:**
- Modify: `deploy/backhaul/params.env.example` (параметры яруса)
- Modify: `deploy/ruvds/backhaul/install.sh` (юнит адаптера `sing-box-bh-anytls`)
- Modify: `deploy/hetzner/backhaul/install.sh` (inbound anytls)

**Interfaces:**
- Consumes: `BACKEND_HOST`, `FRP_SOCKS_VLESS_PORT` (11080), `FRP_SOCKS_MTPROTO_PORT` (11090) — слот primary.
- Produces: `ANYTLS_PORT`, `ANYTLS_PASSWORD`, `ANYTLS_SNI`, `ANYTLS_REALITY_PRIVATE`, `ANYTLS_REALITY_PUBLIC`, `ANYTLS_REALITY_SHORT_ID` в params; сервис `sing-box-bh-anytls` на RuVDS; inbound `anytls` поверх Reality на Hetzner.

- [ ] **Step 1: Добавить параметры**

В `params.env.example` заменить блок `PRIMARY_ENABLED=false` и комментарий про frp на:

```bash
# ────────────────── primary: AnyTLS (RuVDS → Hetzner) ──────────────────
# ВНИМАНИЕ: имя яруса историческое. Слот «primary» резервировался под
# израильский резидентный узел через frp; узла нет, и Realms его не заменяет
# (проблемы CGNAT в этой схеме не было — frpc звонил наружу сам). Слот занят
# транспортом AnyTLS. Имя не переименовано намеренно: Rank задаёт приоритет,
# а переименование инвалидировало бы /var/lib/vpnbot/backhaul-state.json.
#
# AnyTLS решает фингерпринт TLS-in-TLS: собственный фрейминг, padding и mux
# поверх обычного TLS 1.3.
PRIMARY_ENABLED=false

# Порт inbound'а anytls на Hetzner.
ANYTLS_PORT=8768
# Пароль туннеля. Сгенерировать: openssl rand -hex 32
ANYTLS_PASSWORD=""
# AnyTLS поднимается поверх Reality, а НЕ поверх своего сертификата.
# Причина: на Hetzner нет приватного ключа ни для одного нашего имени —
# /etc/letsencrypt отсутствует, у Caddy сертификат только на myvpn-api.online,
# а cdn.moskva.live лежит там лишь слепком tlsfront для эмуляции telemt.
# Reality сертификата не требует: он заимствует чужой у cover-домена.
ANYTLS_SNI="rbc.ru"
# Ключи Reality для туннеля. Сгенерировать: sing-box generate reality-keypair
ANYTLS_REALITY_PRIVATE=""
ANYTLS_REALITY_PUBLIC=""
# Короткий идентификатор. Сгенерировать: openssl rand -hex 8
ANYTLS_REALITY_SHORT_ID=""
# Порты локальных SOCKS5-адаптеров (слот primary).
FRP_SOCKS_VLESS_PORT=11080
FRP_SOCKS_MTPROTO_PORT=11090
```

Удалить из params строки `FRP_VERSION`, `FRP_BIND_PORT`, `FRP_TOKEN` — frp больше не используется.

- [ ] **Step 2: Inbound на Hetzner**

Добавить в `deploy/hetzner/backhaul/install.sh` генерацию `/etc/sing-box-bh/anytls.json`:

```bash
cat > /etc/sing-box-bh/anytls.json <<EOF
{
  "log": { "level": "warn" },
  "inbounds": [{
    "type": "anytls",
    "tag": "bh-anytls-in",
    "listen": "0.0.0.0",
    "listen_port": ${ANYTLS_PORT},
    "users": [{ "name": "bh", "password": "${ANYTLS_PASSWORD}" }],
    "tls": {
      "enabled": true,
      "server_name": "${ANYTLS_SNI}",
      "reality": {
        "enabled": true,
        "handshake": { "server": "${ANYTLS_SNI}", "server_port": 443 },
        "private_key": "${ANYTLS_REALITY_PRIVATE}",
        "short_id": ["${ANYTLS_REALITY_SHORT_ID}"]
      }
    }
  }],
  "outbounds": [{ "type": "direct", "tag": "direct" }]
}
EOF
sing-box check -c /etc/sing-box-bh/anytls.json
```

Юнит `sing-box-bh-anytls.service` — по образцу существующего `sing-box-bhws.service` в том же скрипте.

- [ ] **Step 3: Адаптер на RuVDS**

Добавить в `deploy/ruvds/backhaul/install.sh` генерацию `/etc/sing-box-relay/anytls.json` — два SOCKS5 inbound'а (по одному на класс, чтобы VLESS и MTProto не делили поток) и один anytls outbound:

```bash
cat > /etc/sing-box-relay/anytls.json <<EOF
{
  "log": { "level": "warn" },
  "inbounds": [
    { "type": "socks", "tag": "sk-vless", "listen": "127.0.0.1",
      "listen_port": ${FRP_SOCKS_VLESS_PORT} },
    { "type": "socks", "tag": "sk-mtproto", "listen": "127.0.0.1",
      "listen_port": ${FRP_SOCKS_MTPROTO_PORT} }
  ],
  "outbounds": [{
    "type": "anytls",
    "tag": "to-htz-anytls",
    "server": "${HETZNER_IP}",
    "server_port": ${ANYTLS_PORT},
    "password": "${ANYTLS_PASSWORD}",
    "tls": {
      "enabled": true,
      "server_name": "${ANYTLS_SNI}",
      "utls": { "enabled": true, "fingerprint": "chrome" },
      "reality": {
        "enabled": true,
        "public_key": "${ANYTLS_REALITY_PUBLIC}",
        "short_id": "${ANYTLS_REALITY_SHORT_ID}"
      }
    }
  }]
}
EOF
sing-box check -c /etc/sing-box-relay/anytls.json
```

- [ ] **Step 4: Проверить конфиги обеими сторонами**

```bash
ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110 'sing-box check -c /etc/sing-box-bh/anytls.json && echo HZ-OK'
ssh -i ~/.ssh/russian-vps root@87.247.157.120 '/usr/local/bin/sing-box check -c /etc/sing-box-relay/anytls.json && echo RU-OK'
```

- [ ] **Step 5: Включить ярус и убедиться, что монитор его видит**

```bash
# в params.env: PRIMARY_ENABLED=true
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'cd /tmp/backhaul && MODE=production bash render-config.sh /tmp/params.env && \
   systemctl restart sing-box-bh-anytls sing-box-relay backhaul-monitor && \
   /usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -probe'
```

Ожидается: ярус `primary` здоров, со скоростью выше `PROBE_MIN_BPS`.

- [ ] **Step 6: Сравнить с emergency**

```bash
ssh -i ~/.ssh/russian-vps root@87.247.157.120 'scripts/backhaul/switch.sh status'
```

Записать скорости обоих ярусов. Дальше решает монитор.

- [ ] **Step 7: Коммит**

```bash
git add deploy/backhaul/params.env.example deploy/hetzner/backhaul/install.sh deploy/ruvds/backhaul/install.sh
git commit -m "feat(backhaul): ярус AnyTLS в слоте primary

Слот primary резервировался под израильский резидентный узел через frp.
Узла нет, и Hysteria Realms его не заменяет: проблемы CGNAT в этой схеме
не было — frpc звонил наружу сам, а настоящим блокером был отказ
194.87.80.237 принимать входящий TCP (свойство адреса, не NAT).

Слот занят AnyTLS: собственный фрейминг, padding и mux поверх TLS 1.3,
utls chrome. Константы ярусов НЕ переименованы намеренно — Rank задаёт
приоритет, а переименование инвалидировало бы backhaul-state.json.

frp-параметры удалены за ненадобностью."
```

---

## Task 7: Ярус Hysteria2

Hysteria2 занимает слот `secondary` (`Rank=2`). Слот резервировался под ВМ в Yandex Cloud.

Порядок именно такой (AnyTLS раньше Hysteria2), потому что AnyTLS — TCP, а на этом плече TCP-подобная форма имеет послужной список: в мае 2026 здесь погиб fake-TLS и выжил reality-туннель. Замер UDP от 23.08 — это 35 пакетов; устойчивый многомегабитный QUIC RU→DE непроверен.

**Files:**
- Modify: `deploy/backhaul/params.env.example`
- Modify: `deploy/ruvds/backhaul/install.sh`, `deploy/hetzner/backhaul/install.sh`

**Interfaces:**
- Consumes: `SECONDARY_SOCKS_VLESS_PORT` (11081), `SECONDARY_SOCKS_MTPROTO_PORT` (11091).
- Produces: `HY2_PORT`, `HY2_PASSWORD`, `HY2_OBFS_PASSWORD`, `HY2_UP_MBPS`, `HY2_DOWN_MBPS` в params.

- [ ] **Step 1: Параметры**

Заменить блок secondary в `params.env.example`:

```bash
# ────────────────── secondary: Hysteria2 (RuVDS → Hetzner) ──────────────────
# Имя историческое: слот резервировался под ВМ в Yandex Cloud с WSS.
#
# Hysteria2 поверх QUIC: salamander-обфускация (поток не похож на обычный
# QUIC), Brutal вместо loss-based контроля перегрузки. Трансграничный UDP
# проходит в обе стороны (замер 23.08.2026), но устойчивый многомегабитный
# QUIC RU→DE непроверен — поэтому ярус второй, а не первый.
SECONDARY_ENABLED=false

HY2_PORT=8769
# Пароль. Сгенерировать: openssl rand -hex 32
HY2_PASSWORD=""
# Пароль salamander-обфускации. Сгенерировать: openssl rand -hex 32
HY2_OBFS_PASSWORD=""
# Brutal требует честной оценки полосы. Заниженные значения безопаснее:
# Brutal не отступает при потерях и завышенные цифры забьют канал.
HY2_UP_MBPS=50
HY2_DOWN_MBPS=100
# Имя в сертификате туннеля. Сертификат самоподписанный: Reality для QUIC
# в sing-box не поддерживается, а приватного ключа на наше имя на Hetzner нет.
# Проверку сертификата на клиенте отключаем (insecure) — обе стороны наши, и
# доверие обеспечивается паролем, а не цепочкой. Для DPI это невидимо: в
# TLS 1.3 и QUIC сертификат едет в зашифрованной части рукопожатия.
HY2_SNI="cdn.moskva.live"
SECONDARY_SOCKS_VLESS_PORT=11081
SECONDARY_SOCKS_MTPROTO_PORT=11091
```

Удалить `YC_DOMAIN`, `YC_VM_IP`, `HETZNER_ORIGIN`, `BHWS_PATH`, `BHWS_UUID`, `BHWS_BACKEND_PORT`.

- [ ] **Step 2: Inbound на Hetzner**

```bash
cat > /etc/sing-box-bh/hy2.json <<EOF
{
  "log": { "level": "warn" },
  "inbounds": [{
    "type": "hysteria2",
    "tag": "bh-hy2-in",
    "listen": "0.0.0.0",
    "listen_port": ${HY2_PORT},
    "up_mbps": ${HY2_DOWN_MBPS},
    "down_mbps": ${HY2_UP_MBPS},
    "obfs": { "type": "salamander", "password": "${HY2_OBFS_PASSWORD}" },
    "users": [{ "name": "bh", "password": "${HY2_PASSWORD}" }],
    "masquerade": "https://${HY2_SNI}",
    "tls": {
      "enabled": true,
      "server_name": "${HY2_SNI}",
      "alpn": ["h3"],
      "certificate_path": "/etc/sing-box-bh/hy2.crt",
      "key_path": "/etc/sing-box-bh/hy2.key"
    }
  }],
  "outbounds": [{ "type": "direct", "tag": "direct" }]
}
EOF
sing-box check -c /etc/sing-box-bh/hy2.json
```

`up_mbps`/`down_mbps` на сервере зеркальны клиентским: серверный up — это клиентский down.

- [ ] **Step 3: Адаптер на RuVDS**

```bash
cat > /etc/sing-box-relay/hy2.json <<EOF
{
  "log": { "level": "warn" },
  "inbounds": [
    { "type": "socks", "tag": "sk-vless", "listen": "127.0.0.1",
      "listen_port": ${SECONDARY_SOCKS_VLESS_PORT} },
    { "type": "socks", "tag": "sk-mtproto", "listen": "127.0.0.1",
      "listen_port": ${SECONDARY_SOCKS_MTPROTO_PORT} }
  ],
  "outbounds": [{
    "type": "hysteria2",
    "tag": "to-htz-hy2",
    "server": "${HETZNER_IP}",
    "server_port": ${HY2_PORT},
    "up_mbps": ${HY2_UP_MBPS},
    "down_mbps": ${HY2_DOWN_MBPS},
    "password": "${HY2_PASSWORD}",
    "obfs": { "type": "salamander", "password": "${HY2_OBFS_PASSWORD}" },
    "tls": {
      "enabled": true,
      "server_name": "${HY2_SNI}",
      "alpn": ["h3"],
      "insecure": true
    }
  }]
}
EOF
sing-box check -c /etc/sing-box-relay/hy2.json
```

- [ ] **Step 4: Открыть UDP-порт в Hetzner Cloud Firewall**

Правило `hysteria2` требует UDP, а не TCP. Проверить, что порт открыт именно для UDP:

```bash
ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110 \
  "nft list ruleset | grep -E 'udp dport ${HY2_PORT}' || echo 'ПРАВИЛА НЕТ — добавить'"
```

- [ ] **Step 5: Проверить проходимость до включения яруса**

```bash
ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110 \
  "timeout 25 tcpdump -ni any udp port ${HY2_PORT} -w /tmp/hy2.pcap" &
CAP=$!
sleep 5
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  "for i in \$(seq 1 15); do echo probe > /dev/udp/49.13.201.110/${HY2_PORT}; done"
wait $CAP
ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110 \
  'tcpdump -nr /tmp/hy2.pcap | wc -l; rm -f /tmp/hy2.pcap'
```

Ожидается 15. Важно: считать **все** пакеты, не фильтруя по источнику — RuVDS отправляет с адреса `194.87.80.237`, а не с того, на который к нему обращаются. И держать SSH-сессию tcpdump открытой: фоновый запуск через `nohup` в закрывающейся сессии умирает и даёт ложный ноль.

- [ ] **Step 6: Включить ярус и замерить**

```bash
# в params.env: SECONDARY_ENABLED=true
ssh -i ~/.ssh/russian-vps root@87.247.157.120 \
  'cd /tmp/backhaul && MODE=production bash render-config.sh /tmp/params.env && \
   systemctl restart sing-box-bh-hy2 sing-box-relay backhaul-monitor && \
   scripts/backhaul/switch.sh status'
```

- [ ] **Step 7: Зафиксировать сравнение ярусов**

Записать в `docs/backhaul-triple.md` фактические скорости трёх ярусов из `switch.sh status` — с датой замера. Это единственное честное основание для выбора приоритетов.

- [ ] **Step 8: Коммит**

```bash
git add deploy/backhaul/params.env.example deploy/hetzner/backhaul/install.sh \
        deploy/ruvds/backhaul/install.sh docs/backhaul-triple.md
git commit -m "feat(backhaul): ярус Hysteria2 в слоте secondary

Salamander-обфускация, masquerade на наш домен, Brutal вместо loss-based
контроля перегрузки. Слот secondary резервировался под ВМ в Yandex Cloud
с WSS; ВМ не создавалась, параметры YC/BHWS удалены.

Ярус второй, а не первый, намеренно: AnyTLS — TCP, а на этом плече
TCP-подобная форма имеет послужной список (май 2026: fake-TLS погиб,
reality-туннель выжил). Замер UDP от 23.08 — 35 пакетов; устойчивый
многомегабитный QUIC RU→DE остаётся непроверенным до задачи 7.

Приоритет между ярусами дальше определяет backhaul-monitor по фактической
скорости, а не наше предположение."
```

---

## Самопроверка плана

**Покрытие спеки.** Этап 0 → задача 1. Четыре уровня отката → задача 1 (уровни 2, 3) и уровень 1 (`systemctl restart nftables`) как аварийная команда в `do_rollback`; уровень 4 обеспечен запретом писать в `/etc/nftables.conf`, зафиксированным в Global Constraints. Этап 1 → задачи 2 и 4. Исправление списка портов → задача 2. Этап 2 (канарейка) → задачи 3 и 5. Этап 3 (ярусы) → задачи 6 и 7. Вопрос спеки про переименование ярусов решён в задаче 6: не переименовывать, с обоснованием.

**Отклонение от спеки.** Спека предполагала переименование констант ярусов в транспорт-нейтральные. План отказывается: `Rank` задаёт приоритет, имя — идентификатор, а переименование инвалидирует ключи в `backhaul-state.json` и тянет правки в монитор, `switch.sh` и генератор. Вместо этого имена документированы как исторические.

**Находки, которых в спеке не было** (обнаружены при чтении `params.env.example`): порт 443 в `RELAY_VLESS_PORTS` при том, что его держит nginx — уронил бы весь relay; `RUVDS_IP` указывал на заблокированный адрес. Обе вошли в задачу 2.
