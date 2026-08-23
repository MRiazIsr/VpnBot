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

cleanup() { nft delete table ip bh_drill 2>/dev/null || true; rm -f "$SNAP" /tmp/bh_drill-restore.nft; }
trap cleanup EXIT

nft delete table ip bh_drill 2>/dev/null || true
nft add table ip bh_drill
nft add chain ip bh_drill prerouting '{ type nat hook prerouting priority dstnat; policy accept; }'
nft add rule ip bh_drill prerouting tcp dport 65531 dnat to 127.0.0.1:65532
nft add rule ip bh_drill prerouting tcp dport 65533 dnat to 127.0.0.1:65534

# `|| true` обязателен: grep -c без совпадений печатает 0 и выходит с кодом 1,
# а под `set -e` + pipefail это уронило бы скрипт прямо на присваивании — с
# кодом 1 и без единого слова о причине. Проверка ниже никогда бы не
# выполнилась, то есть ровно в случае реального провала прогон молчал бы.
before=$(nft list table ip bh_drill | grep -c dnat || true)
[[ "$before" -eq 2 ]] || fail "не удалось создать служебные правила (найдено: ${before})"

nft list table ip bh_drill > "$SNAP"
[[ -s "$SNAP" ]] || fail "снимок пустой"
grep -q dnat "$SNAP" || fail "в снимке нет правил dnat"
ok "снимок снят, правил: $before"

nft delete table ip bh_drill
nft list table ip bh_drill >/dev/null 2>&1 && fail "таблица не удалилась"
ok "таблица удалена (имитация promote)"

nft -f "$SNAP"
after=$(nft list table ip bh_drill | grep -c dnat || true)
[[ "$after" -eq "$before" ]] || fail "восстановлено $after правил вместо $before"
ok "восстановлено правил: $after — откат работает"

# Тот же откат, но в той форме, в которой его делает promote.sh: таблица ЖИВА
# и содержит переведённое состояние, а вернуть надо снимок — одной транзакцией.
# add нужен, чтобы delete не уронил батч, если таблицы вдруг нет; delete + всё
# определение из снимка идут тем же батчем, поэтому промежутка «правил нет
# вовсе» не существует. Здесь проверяется в том числе форма файла снимка:
# `nft list table` даёт ровно то, что `nft -f` принимает обратно.
handle=$(nft -a list table ip bh_drill | awk '/dport 65531 dnat/ {print $NF; exit}')
[[ -n "$handle" ]] || fail "не нашли handle правила для имитации перевода"
nft replace rule ip bh_drill prerouting handle "$handle" tcp dport 65531 redirect to :65535
redirects=$(nft list table ip bh_drill | grep -c redirect || true)
[[ "$redirects" -eq 1 ]] || fail "имитация перевода не удалась: redirect'ов ${redirects}"
ok "имитация перевода: dnat заменён на redirect одной командой nft replace"

RESTORE=/tmp/bh_drill-restore.nft
{ printf 'add table ip bh_drill\ndelete table ip bh_drill\n'; cat "$SNAP"; } > "$RESTORE"
nft -f "$RESTORE"
rm -f "$RESTORE"
back=$(nft list table ip bh_drill | grep -c dnat || true)
still=$(nft list table ip bh_drill | grep -c redirect || true)
[[ "$back" -eq "$before" ]] || fail "атомарный откат вернул $back правил вместо $before"
[[ "$still" -eq 0 ]] || fail "после атомарного отката остался redirect"
ok "атомарный откат (add+delete+снимок одним nft -f) вернул правил: $back"

# Тот же прогон старым способом: должен показать, почему он не работал.
if iptables-save -t nat 2>/dev/null | grep -q 65531; then
  fail "iptables-save неожиданно видит nft-правила — пересмотреть вывод задачи"
fi
ok "iptables-save правил не видит — подтверждение исходного дефекта"
