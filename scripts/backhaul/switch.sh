#!/usr/bin/env bash
# Ручное управление маршрутом. Запускать НА RuVDS.
#
#   ./switch.sh status                      # что сейчас активно и почему
#   ./switch.sh vless secondary             # пин: VLESS → secondary
#   ./switch.sh mtproto emergency           # пин: MTProto → emergency
#   ./switch.sh vless auto                  # снять пин, вернуть автоматику
#   ./switch.sh dry-run                     # один цикл решений без переключений
#   ./switch.sh logs [N]                    # последние N решений монитора
#
# Пин переживает рестарт монитора: он записан в файл состояния.
set -euo pipefail

CFG=/etc/vpnbot/backhaul.json
BIN=/usr/local/bin/backhaul-monitor
STATE=/var/lib/vpnbot/backhaul-state.json
LOG=/var/log/backhaul-monitor.jsonl

usage() { sed -n '2,15p' "$0" | sed 's/^# \?//'; exit 1; }

cmd="${1:-status}"

case "$cmd" in
  status)
    echo "── selector'ы (живое состояние sing-box) ──"
    LISTEN="$(python3 -c "import json;print(json.load(open('$CFG'))['clash_api']['listen'])")"
    SECRET="$(python3 -c "import json;print(json.load(open('$CFG'))['clash_api']['secret'])")"
    for sel in sel-vless sel-mtproto; do
      curl -sS --max-time 5 -H "Authorization: Bearer ${SECRET}" \
        "http://${LISTEN}/proxies/${sel}" 2>/dev/null \
      | python3 -c "
import json,sys
try:
    p=json.load(sys.stdin)
    print(f\"  {p['name']}: сейчас {p['now']}   из {p['all']}\")
except Exception:
    print('  ${sel}: API не ответил')
"
    done
    echo
    echo "── состояние монитора (гистерезис, пины) ──"
    [[ -r "$STATE" ]] && python3 - <<PY
import json
st=json.load(open("$STATE"))
for cls,c in st.get("classes",{}).items():
    pin = f"  ПИН={c['forced']}" if c.get("forced") else ""
    print(f"  {cls}: активен {c.get('active') or '—'}{pin}   последнее переключение {c.get('last_switch_at','—')}")
    for name,t in sorted(c.get("tiers",{}).items()):
        mark = "живой" if t.get("healthy") else "МЁРТВ"
        extra = f", последняя ошибка: {t['last_reason']}" if t.get("last_reason") else ""
        speed = ""
        if t.get("last_down_bps"):
            speed = f", {int(t['last_down_bps'])//1024} KB/s вниз / {int(t.get('last_up_bps',0))//1024} KB/s вверх"
        print(f"      {name:<10} {mark}  ok={t.get('consec_ok',0)} fail={t.get('consec_fail',0)}{speed}{extra}")
PY
    ;;

  vless|mtproto)
    tier="${2:?укажите tier: primary|secondary|emergency|auto}"
    if [[ "$tier" == "auto" ]]; then
      "$BIN" -config "$CFG" -force "${cmd}="
      echo "пин снят: ${cmd} снова управляется автоматикой"
    else
      "$BIN" -config "$CFG" -force "${cmd}=${tier}"
      echo "${cmd} прибит к ${tier}; снять: $0 ${cmd} auto"
    fi
    ;;

  dry-run)
    "$BIN" -config "$CFG" -once -dry-run
    ;;

  probe)
    "$BIN" -config "$CFG" -probe
    ;;

  logs)
    n="${2:-40}"
    [[ -r "$LOG" ]] || { echo "нет $LOG"; exit 1; }
    tail -n 2000 "$LOG" | python3 - "$n" <<'PY'
import json,sys
n=int(sys.argv[1]); rows=[]
for line in sys.stdin:
    try: r=json.loads(line)
    except Exception: continue
    if r.get("msg") in ("МАРШРУТ ПЕРЕКЛЮЧЁН","изменилось здоровье backhaul'а",
                        "переключение придержано","РУЧНОЕ ПЕРЕКЛЮЧЕНИЕ",
                        "переключение не удалось"):
        rows.append(r)
for r in rows[-n:]:
    t=r.get("time","")[:19]
    print(f"{t}  {r.get('msg')}  " + " ".join(
        f"{k}={v}" for k,v in r.items() if k not in ("time","level","msg")))
PY
    ;;

  *) usage ;;
esac
