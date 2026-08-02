#!/usr/bin/env bash
# Полная проверка тройного backhaul. Запускать НА RuVDS.
#
# Проверяет то, что реально может сломаться, а не то, что удобно проверять:
# не ping и не tcp-connect, а перекачку данных в обе стороны, длительные и
# параллельные соединения, независимость selector'ов и отсутствие публично
# доступных служебных портов.
#
#   ./verify.sh /etc/backhaul/params.env            # всё
#   ./verify.sh /etc/backhaul/params.env exposure   # только проверку портов
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
ONLY="${2:-all}"
# shellcheck disable=SC1090
source "$PARAMS"

CFG=/etc/vpnbot/backhaul.json
PASS=0; FAIL=0
ok()   { printf '\033[1;32m  PASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '\033[1;31m  FAIL\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }
head_() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }

jqget() { python3 -c "import json,sys;print(json.load(open('$CFG'))$1)"; }

BACKEND="$(jqget "['backend_host']")"
API_LISTEN="$(jqget "['clash_api']['listen']")"
API_SECRET="$(jqget "['clash_api']['secret']")"
PROBE_URL="$(jqget "['probe']['base_url']")"

api() { curl -sS --max-time 5 -H "Authorization: Bearer ${API_SECRET}" "http://${API_LISTEN}$1"; }

# ─────────────────── 1. каждый backhaul по отдельности ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "probe" ]]; then
head_ "1. Каждый backhaul отдельно (>128KB в обе стороны, скорость, зависание)"
/usr/local/bin/backhaul-monitor -config "$CFG" -probe > /tmp/bh-probe.json 2>&1 && PROBE_RC=0 || PROBE_RC=$?

# Разбираем поток JSON-объектов один раз: и провалы, и объём переданного.
PROBE_REPORT="$(python3 - <<'PY'
import json
txt = open("/tmp/bh-probe.json").read()
dec = json.JSONDecoder()
i = 0
failed, small, seen = [], [], 0
while i < len(txt):
    while i < len(txt) and txt[i] != "{":
        i += 1
    if i >= len(txt):
        break
    try:
        obj, i = dec.raw_decode(txt, i)
    except ValueError:
        i += 1
        continue
    seen += 1
    name = f"{obj.get('tier')}/{obj.get('class')}"
    if not obj.get("ok"):
        failed.append(f"{name}: {obj.get('phase')} — {obj.get('reason')}")
    elif obj.get("down_bytes", 0) < 131072 or obj.get("up_bytes", 0) < 131072:
        small.append(f"{name}: down={obj.get('down_bytes')} up={obj.get('up_bytes')}")
print(json.dumps({"seen": seen, "failed": failed, "small": small}))
PY
)"
SEEN="$(printf '%s' "$PROBE_REPORT"    | python3 -c 'import json,sys;print(json.load(sys.stdin)["seen"])')"
FAILED="$(printf '%s' "$PROBE_REPORT"  | python3 -c 'import json,sys;print("\n".join(json.load(sys.stdin)["failed"]))')"
SMALL="$(printf '%s' "$PROBE_REPORT"   | python3 -c 'import json,sys;print("\n".join(json.load(sys.stdin)["small"]))')"

if [[ "$SEEN" -eq 0 ]]; then
  bad "проверка не дала результатов — см. /tmp/bh-probe.json"
elif [[ -z "$FAILED" ]]; then
  ok "все включённые backhaul'ы прошли проверку (${SEEN} шт.)"
else
  bad "не все backhaul'ы живы:"
  printf '    %s\n' "$FAILED"
fi
if [[ -n "$SMALL" ]]; then
  bad "передано меньше 128 KB:"
  printf '    %s\n' "$SMALL"
else
  [[ "$SEEN" -gt 0 ]] && ok "везде передано ≥128 KB в обе стороны"
fi
fi

# ─────────────────── 1b. прямые профили не задеты ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "direct" ]]; then
head_ "1b. Прямые профили клиент→Hetzner живы и не заведены на relay"
HETZ="${HETZNER_IP:-49.13.201.110}"
for p in ${PROTECTED_DIRECT_PORTS:-4443 8444 8447}; do
  # а) порт по-прежнему принимает соединения напрямую на Hetzner
  if timeout 6 bash -c "exec 3<>/dev/tcp/${HETZ}/${p}" 2>/dev/null; then
    ok "Hetzner:${p} отвечает напрямую"
  else
    bad "Hetzner:${p} НЕ отвечает — прямой профиль сломан"
  fi
  # б) и этот порт не подхвачен relay на RuVDS
  if ss -tlnH "sport = :${p}" 2>/dev/null | grep -q .; then
    bad "порт ${p} слушается на RuVDS — прямой профиль уведён на relay, так нельзя"
  fi
done
fi

# ─────────────────── 2. локальные адаптеры — только loopback ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "exposure" ]]; then
head_ "2. SOCKS/API/backend не доступны снаружи"
LOOPBACK_ONLY=true
while read -r addr; do
  case "$addr" in
    127.0.0.1:*|\[::1\]:*) : ;;
    *) LOOPBACK_ONLY=false; echo "    наружу слушает: $addr" ;;
  esac
done < <(ss -tlnH | awk '{print $4}' | grep -E ':(1108[0-9]|1109[0-9]|19090)$' || true)
$LOOPBACK_ONLY && ok "все адаптеры и API только на loopback" \
                || bad "служебный порт торчит наружу"

# Проверка снаружи: адрес самой машины, а не loopback.
PUBIP="$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -1)"
EXPOSED=0
for p in ${FRP_SOCKS_VLESS_PORT} ${FRP_SOCKS_MTPROTO_PORT} \
         ${SECONDARY_SOCKS_VLESS_PORT} ${SECONDARY_SOCKS_MTPROTO_PORT} \
         ${EMERGENCY_SOCKS_VLESS_PORT} ${EMERGENCY_SOCKS_MTPROTO_PORT} \
         "${CLASH_API_LISTEN##*:}"; do
  if timeout 3 bash -c "exec 3<>/dev/tcp/${PUBIP}/${p}" 2>/dev/null; then
    bad "порт ${p} отвечает на публичном адресе ${PUBIP}"
    EXPOSED=1
  fi
done
[[ $EXPOSED -eq 0 ]] && ok "ни один служебный порт не отвечает на ${PUBIP}"

head_ "2b. nftables: default deny"
if nft list chain inet backhaul input 2>/dev/null | grep -q 'policy drop'; then
  ok "input policy drop"
else
  bad "input policy не drop — фаервол не в целевом состоянии"
fi
fi

# ─────────────────── 3. remote DNS через SOCKS ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "dns" ]]; then
head_ "3. Remote DNS через SOCKS5"
DNS_OK=0
for port in ${SECONDARY_SOCKS_VLESS_PORT} ${EMERGENCY_SOCKS_VLESS_PORT}; do
  ss -tlnH "sport = :${port}" | grep -q . || continue
  # --socks5-hostname = имя резолвит удалённая сторона, а не мы.
  if curl -sS --max-time 10 --socks5-hostname "127.0.0.1:${port}" \
       -o /dev/null -w '' https://www.google.com/generate_204 2>/dev/null; then
    ok "адаптер :${port} резолвит имена на своей стороне"
    DNS_OK=1
  else
    bad "адаптер :${port} не смог сделать remote-DNS запрос"
  fi
done
[[ $DNS_OK -eq 0 ]] && echo "    (ни один адаптер не поднят — проверка пропущена)"
fi

# ─────────────────── 4. длительные и параллельные соединения ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "load" ]]; then
head_ "4. Параллельные и длительные соединения"
for port in ${SECONDARY_SOCKS_VLESS_PORT} ${EMERGENCY_SOCKS_VLESS_PORT}; do
  ss -tlnH "sport = :${port}" | grep -q . || continue
  # 8 параллельных закачек по 1 MB: проверяем и параллелизм, и что поток
  # не схлопнут в один мультиплекс.
  fails=0
  for i in $(seq 1 8); do
    ( curl -sS --max-time 60 --socks5 "127.0.0.1:${port}" \
        -o /dev/null "${PROBE_URL}/down?bytes=1048576" || echo "fail" >> /tmp/bh-par.$$ ) &
  done
  wait
  [[ -f /tmp/bh-par.$$ ]] && fails=$(wc -l < /tmp/bh-par.$$) && rm -f /tmp/bh-par.$$
  if [[ "$fails" -eq 0 ]]; then
    ok ":${port} — 8 параллельных закачек по 1 MB прошли"
  else
    bad ":${port} — ${fails} из 8 параллельных закачек упали"
  fi

  # Длительное соединение: 16 MB одним потоком, с контролем зависания.
  if curl -sS --max-time 180 --speed-time 20 --speed-limit 4096 \
       --socks5 "127.0.0.1:${port}" -o /dev/null "${PROBE_URL}/down?bytes=16777216"; then
    ok ":${port} — длительная передача 16 MB без зависаний"
  else
    bad ":${port} — длительная передача оборвалась или повисла"
  fi
done
fi

# ─────────────────── 5. selector'ы и независимость ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "selector" ]]; then
head_ "5. Независимость selector'ов"
V_NOW="$(api /proxies/sel-vless   | python3 -c 'import json,sys;print(json.load(sys.stdin)["now"])' 2>/dev/null || echo '?')"
M_NOW="$(api /proxies/sel-mtproto | python3 -c 'import json,sys;print(json.load(sys.stdin)["now"])' 2>/dev/null || echo '?')"
echo "    sel-vless=${V_NOW}  sel-mtproto=${M_NOW}"
if [[ "$V_NOW" == "?" || "$M_NOW" == "?" ]]; then
  bad "локальный API sing-box не отвечает"
else
  ok "оба selector'а доступны через локальный API"
  # Переключаем только vless и убеждаемся, что mtproto не сдвинулся.
  MEMBERS="$(api /proxies/sel-vless | python3 -c 'import json,sys;print(" ".join(json.load(sys.stdin)["all"]))')"
  OTHER="$(echo "$MEMBERS" | tr ' ' '\n' | grep -v "^${V_NOW}$" | head -1 || true)"
  if [[ -n "$OTHER" ]]; then
    curl -sS --max-time 5 -X PUT -H "Authorization: Bearer ${API_SECRET}" \
      -H 'Content-Type: application/json' -d "{\"name\":\"${OTHER}\"}" \
      "http://${API_LISTEN}/proxies/sel-vless" >/dev/null
    M_AFTER="$(api /proxies/sel-mtproto | python3 -c 'import json,sys;print(json.load(sys.stdin)["now"])')"
    if [[ "$M_AFTER" == "$M_NOW" ]]; then
      ok "переключение vless не задело mtproto"
    else
      bad "переключение vless сдвинуло mtproto (${M_NOW} → ${M_AFTER})"
    fi
    curl -sS --max-time 5 -X PUT -H "Authorization: Bearer ${API_SECRET}" \
      -H 'Content-Type: application/json' -d "{\"name\":\"${V_NOW}\"}" \
      "http://${API_LISTEN}/proxies/sel-vless" >/dev/null
  fi
fi
fi

# ─────────────────── 6. боевые порты слушает relay ───────────────────
if [[ "$ONLY" == "all" || "$ONLY" == "ports" ]]; then
head_ "6. Порты relay"
MISSING=0
for p in $(python3 -c "import json;print(' '.join(str(x['listen_port']) for x in json.load(open('$CFG'))['ports']))"); do
  ss -tlnH "sport = :${p}" | grep -q . || { echo "    не слушается: ${p}"; MISSING=1; }
done
[[ $MISSING -eq 0 ]] && ok "все порты из backhaul.json слушаются" || bad "часть портов не поднята"
fi

printf '\n\033[1m Итог: PASS=%d FAIL=%d\033[0m\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
