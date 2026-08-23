#!/usr/bin/env bash
# Генерация /etc/vpnbot/backhaul.json и /etc/sing-box-relay/config.json.
#
# Один источник правды для install.sh и promote.sh: конфиг собирается ровно
# одним кодом, поэтому боевой и staging-режимы не могут разъехаться.
#
#   MODE=staging    ./render-config.sh params.env   # порты со смещением
#   MODE=production ./render-config.sh params.env   # боевые порты
set -euo pipefail

PARAMS="${1:-/etc/backhaul/params.env}"
[[ -r "$PARAMS" ]] || { echo "нет файла параметров: $PARAMS" >&2; exit 1; }
# shellcheck disable=SC1090
source "$PARAMS"

MODE="${MODE:-staging}"
mkdir -p /etc/vpnbot "${SINGBOX_RELAY_DIR:-/etc/sing-box-relay}" /var/lib/vpnbot

# ─────────────────────── защита прямых профилей ───────────────────────
# DE-VK (4443), DE-Yandex (8444), DE-Sber (8447) ходят напрямую в Hetzner мимо
# RuVDS. На 02.08.2026 это единственные работающие профили. Завести их на relay
# значит подчинить единственный живой путь состоянию RuVDS — то есть ровно
# то, чего делать нельзя. Проверка жёсткая: правка списка портов не должна
# уметь сломать это по невнимательности.
PROTECTED_DIRECT_PORTS="${PROTECTED_DIRECT_PORTS:-4443 8444 8447}"
for prot in $PROTECTED_DIRECT_PORTS; do
  for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
    if [[ "$p" == "$prot" ]]; then
      echo "ОТКАЗ: порт $prot попал в список relay." >&2
      echo "  Это прямой профиль клиент→Hetzner в обход RuVDS, он должен остаться нетронутым." >&2
      echo "  Уберите $prot из RELAY_VLESS_PORTS/RELAY_MTPROTO_PORTS в params.env." >&2
      exit 1
    fi
  done
done

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
#
# grep без ^-якоря: `ss -tlnp` кладёт имя процесса не первым полем, а внутрь
# users:(("sing-box",pid=...,fd=...)) — якорь на начало строки никогда бы не
# совпал, и повторный запуск render-config.sh на уже поднятом relay ложно
# отказывал бы из-за собственного sing-box.
for p in ${RELAY_VLESS_PORTS} ${RELAY_MTPROTO_PORTS}; do
  listen_port="$p"
  [[ "$MODE" == "staging" ]] && listen_port=$(( p + STAGING_PORT_OFFSET ))
  holder=$(ss -tlnp "sport = :${listen_port}" 2>/dev/null \
    | awk 'NR>1 {print $NF}' | grep -v 'sing-box' | head -1 || true)
  if [[ -n "$holder" ]]; then
    echo "ОТКАЗ: порт $listen_port уже занят: $holder" >&2
    echo "  sing-box не забиндит его и не поднимется — упадут ВСЕ relay-порты." >&2
    echo "  Уберите $p из RELAY_*_PORTS либо освободите порт." >&2
    exit 1
  fi
done

# Плечо существует ровно тогда, когда существует его физическая опора:
# primary — израильский узел, secondary — ВМ в Yandex Cloud. Выключенное
# плечо не попадает ни в конфиг sing-box, ни в опрос монитора.
[[ "${PRIMARY_ENABLED:-false}"   == "true" ]] && PRIMARY_DISABLED_JSON=false   || PRIMARY_DISABLED_JSON=true
[[ "${SECONDARY_ENABLED:-false}" == "true" ]] && SECONDARY_DISABLED_JSON=false || SECONDARY_DISABLED_JSON=true

if [[ "$PRIMARY_DISABLED_JSON" == "true" && "$SECONDARY_DISABLED_JSON" == "true" ]]; then
  echo "внимание: включён только emergency — резерва нет, это стартовая конфигурация" >&2
fi

MODE="$MODE" PRIMARY_DISABLED_JSON="$PRIMARY_DISABLED_JSON" \
SECONDARY_DISABLED_JSON="$SECONDARY_DISABLED_JSON" \
STAGING_PORT_OFFSET="$STAGING_PORT_OFFSET" \
RELAY_VLESS_PORTS="$RELAY_VLESS_PORTS" RELAY_MTPROTO_PORTS="$RELAY_MTPROTO_PORTS" \
BACKEND_HOST="$BACKEND_HOST" \
FRP_SOCKS_VLESS_PORT="$FRP_SOCKS_VLESS_PORT" FRP_SOCKS_MTPROTO_PORT="$FRP_SOCKS_MTPROTO_PORT" \
SECONDARY_SOCKS_VLESS_PORT="$SECONDARY_SOCKS_VLESS_PORT" SECONDARY_SOCKS_MTPROTO_PORT="$SECONDARY_SOCKS_MTPROTO_PORT" \
EMERGENCY_SOCKS_VLESS_PORT="$EMERGENCY_SOCKS_VLESS_PORT" EMERGENCY_SOCKS_MTPROTO_PORT="$EMERGENCY_SOCKS_MTPROTO_PORT" \
CLASH_API_LISTEN="$CLASH_API_LISTEN" CLASH_API_SECRET="$CLASH_API_SECRET" \
PROBE_LISTEN="$PROBE_LISTEN" PROBE_DOWNLOAD_BYTES="$PROBE_DOWNLOAD_BYTES" \
PROBE_UPLOAD_BYTES="$PROBE_UPLOAD_BYTES" PROBE_TIMEOUT_SEC="$PROBE_TIMEOUT_SEC" \
PROBE_STALL_SEC="$PROBE_STALL_SEC" PROBE_MIN_BPS="$PROBE_MIN_BPS" \
PROBE_INTERVAL_SEC="$PROBE_INTERVAL_SEC" PROBE_FAIL_THRESHOLD="$PROBE_FAIL_THRESHOLD" \
PROBE_RECOVER_THRESHOLD="$PROBE_RECOVER_THRESHOLD" PROBE_HOLD_DOWN_SEC="$PROBE_HOLD_DOWN_SEC" \
PROBE_STABLE_BEFORE_RETURN_SEC="$PROBE_STABLE_BEFORE_RETURN_SEC" \
SINGBOX_RELAY_DIR="${SINGBOX_RELAY_DIR:-/etc/sing-box-relay}" \
python3 <<'PY'
import json, os

env = os.environ
mode = env["MODE"]
staged = (mode == "staging")
offset = int(env["STAGING_PORT_OFFSET"])

def entries(ports, cls):
    out = []
    for p in (int(x) for x in ports.split()):
        listen = p + offset if staged else p
        out.append({
            "tag": f"in-{cls}-{listen}",
            "class": cls,
            "listen_port": listen,
            # Backend — тот же номер порта на Hetzner. Основной sing-box и telemt
            # там уже слушают на всех адресах, значит и на адресе wg1. Дублировать
            # inbound'ы с теми же ключами Reality не нужно и рискованно.
            "target_port": p,
            "comment": ("staging " if staged else "") + f"{cls} {p}",
        })
    return out

ports = entries(env["RELAY_VLESS_PORTS"], "vless") + \
        entries(env["RELAY_MTPROTO_PORTS"], "mtproto")

cfg = {
    "relay_listen": "0.0.0.0",
    "backend_host": env["BACKEND_HOST"],
    "tiers": [
        {"name": "primary", "rank": 1,
         "disabled": env["PRIMARY_DISABLED_JSON"] == "true",
         "socks_port": {"vless": int(env["FRP_SOCKS_VLESS_PORT"]),
                        "mtproto": int(env["FRP_SOCKS_MTPROTO_PORT"])},
         "extra": {"transport": "frp/TLS <- IL residential -> wg1 -> Hetzner"}},
        {"name": "secondary", "rank": 2,
         "disabled": env["SECONDARY_DISABLED_JSON"] == "true",
         "socks_port": {"vless": int(env["SECONDARY_SOCKS_VLESS_PORT"]),
                        "mtproto": int(env["SECONDARY_SOCKS_MTPROTO_PORT"])},
         "extra": {"transport": "vless+ws+tls -> Yandex Cloud -> Hetzner"}},
        {"name": "emergency", "rank": 3,
         "socks_port": {"vless": int(env["EMERGENCY_SOCKS_VLESS_PORT"]),
                        "mtproto": int(env["EMERGENCY_SOCKS_MTPROTO_PORT"])},
         "extra": {"transport": "ssh -D (инициирует RuVDS, выход на Hetzner)"}},
    ],
    "ports": ports,
    "clash_api": {"listen": env["CLASH_API_LISTEN"], "secret": env["CLASH_API_SECRET"]},
    "probe": {
        "base_url": "http://" + env["PROBE_LISTEN"],
        "download_bytes": int(env["PROBE_DOWNLOAD_BYTES"]),
        "upload_bytes": int(env["PROBE_UPLOAD_BYTES"]),
        "timeout_sec": int(env["PROBE_TIMEOUT_SEC"]),
        "stall_sec": int(env["PROBE_STALL_SEC"]),
        "min_bytes_per_sec": int(env["PROBE_MIN_BPS"]),
        "check_remote_dns": False,
        "dns_probe_host": "",
    },
    "monitor": {
        "interval_sec": int(env["PROBE_INTERVAL_SEC"]),
        "fail_threshold": int(env["PROBE_FAIL_THRESHOLD"]),
        "recover_threshold": int(env["PROBE_RECOVER_THRESHOLD"]),
        "hold_down_sec": int(env["PROBE_HOLD_DOWN_SEC"]),
        "stable_before_return_sec": int(env["PROBE_STABLE_BEFORE_RETURN_SEC"]),
    },
    "state_path": "/var/lib/vpnbot/backhaul-state.json",
    "log_path": "/var/log/backhaul-monitor.jsonl",
    "relay_config_path": env["SINGBOX_RELAY_DIR"] + "/config.json",
}

with open("/etc/vpnbot/backhaul.json", "w") as f:
    json.dump(cfg, f, indent=2, ensure_ascii=False)
    f.write("\n")
active = [t["name"] for t in cfg["tiers"] if not t.get("disabled")]
print(f"backhaul.json: режим={mode}, портов={len(ports)}, активные плечи: {', '.join(active)}")
PY

chmod 600 /etc/vpnbot/backhaul.json

/usr/local/bin/backhaul-monitor -config /etc/vpnbot/backhaul.json -render-relay \
  > "${SINGBOX_RELAY_DIR:-/etc/sing-box-relay}/config.json"
chmod 600 "${SINGBOX_RELAY_DIR:-/etc/sing-box-relay}/config.json"

# Требование: после изменений обязательно sing-box check.
"${SINGBOX_RELAY_BIN:-/usr/local/bin/sing-box}" check \
  -c "${SINGBOX_RELAY_DIR:-/etc/sing-box-relay}/config.json"
echo "sing-box check: OK"
