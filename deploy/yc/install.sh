#!/usr/bin/env bash
# Установка WSS-релея на виртуалке Yandex Cloud (ru-central1).
#
# RuVDS --wss--> ЭТА ВМ --wss--> Hetzner (Caddy :8443) --> sing-box-bhws
#
# Домен наш, сертификат наш (Let's Encrypt через Caddy). Чужих SNI и доменов
# в цепочке нет.
#
# Запуск на ВМ, от root:
#   ./install.sh /path/to/params.env
set -euo pipefail

PARAMS="${1:?укажите params.env}"
# shellcheck disable=SC1090
source "$PARAMS"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }

for v in YC_DOMAIN HETZNER_ORIGIN BHWS_PATH; do
  [[ -n "${!v:-}" ]] || { echo "параметр $v не задан" >&2; exit 1; }
done

HETZNER_HOST="${HETZNER_ORIGIN%%:*}"

log "Caddy"
if ! command -v caddy >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl >/dev/null
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
  apt-get update -qq
  apt-get install -y caddy >/dev/null
fi

STAMP="$(date -u +%Y%m%d-%H%M%S)"
[[ -f /etc/caddy/Caddyfile ]] && cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak-${STAMP}"

cat > /etc/caddy/Caddyfile <<EOF
# WSS-плечо backhaul: Yandex Cloud → Hetzner.
# Caddy сам получает и продлевает сертификат для нашего домена.
${YC_DOMAIN} {
	handle ${BHWS_PATH} {
		reverse_proxy https://${HETZNER_ORIGIN} {
			# Origin отвечает на своём имени — Host и SNI выставляем явно,
			# иначе Caddy Hetzner не найдёт сайт и вернёт 404.
			header_up Host ${HETZNER_HOST}
			transport http {
				tls
				tls_server_name ${HETZNER_HOST}
				dial_timeout 10s
				# Никаких таймаутов на чтение/запись: соединение долгоживущее,
				# и обрывать его по времени нельзя.
				keepalive 2m
			}
		}
	}

	# Всё остальное — обычная заглушка. Ничего лишнего наружу не отдаём.
	handle {
		respond "Not Found" 404
	}

	log {
		output file /var/log/caddy/bhws.log
		level WARN
	}
}
EOF

caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
systemctl enable --now caddy
systemctl reload caddy || systemctl restart caddy

log "готово. Проверка апгрейда:"
cat <<EOF
  curl -sS -i -N --http1.1 \\
    -H 'Connection: Upgrade' -H 'Upgrade: websocket' \\
    -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Version: 13' \\
    https://${YC_DOMAIN}${BHWS_PATH} | head -1
  # ожидается: HTTP/1.1 101 Switching Protocols
EOF
