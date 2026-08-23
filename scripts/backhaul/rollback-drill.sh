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
