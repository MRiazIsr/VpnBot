# zapret/nfqws deploy на RuVDS

Server-side обфускация исходящих TCP-пакетов через DPI-desync (nfqws) — обход поведенческого DPI ТСПУ для direct-exit VLESS-инбаундов.

## Установка

```bash
# 1. Клонировать и собрать zapret (bol-van/zapret)
apt install -y build-essential gcc libnetfilter-queue-dev
git clone https://github.com/bol-van/zapret /opt/zapret
cd /opt/zapret && make -C nfq

# 2. Установить systemd unit
cp /root/vpn-backend-tg-bot/deploy/ruvds/zapret/nfqws.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now nfqws.service
systemctl status nfqws

# 3. Применить nftables правила
cp /root/vpn-backend-tg-bot/deploy/ruvds/zapret/nftables-nfqws.rules /etc/nftables-nfqws.rules
nft -f /etc/nftables-nfqws.rules
nft list ruleset | grep -A 4 'table inet zapret'
```

## Верификация

```bash
# Пакеты идут в NFQUEUE
nft list ruleset | grep -A 4 'table inet zapret'

# systemd активен
systemctl is-active nfqws

# Логи без ошибок
journalctl -u nfqws -n 100 --no-pager

# Счётчик пакетов растёт при трафике
nft list ruleset | grep -B 1 'queue num 100'
```

Проверка wg-туннеля к Hetzner: с любого существующего inbound (не direct-exit) сделать `curl ipinfo.io/json` — должен показывать Hetzner IP (49.13.201.110). Правила фильтруют только `oifname "eth0"`, wg-трафик идёт через wg0 и не трогается.

## Откат

```bash
systemctl stop nfqws
systemctl disable nfqws
nft delete table inet zapret
```

Восстановление сети — 5-10 секунд.

## Тюнинг

Если частые false-positive (сайты перестали открываться): увеличить `--dpi-desync-split-pos` до 4-8 или сменить `--dpi-desync=split` на `--dpi-desync=fake,split`. Правки — в `/etc/systemd/system/nfqws.service`, затем `systemctl daemon-reload && systemctl restart nfqws`.

## Границы применимости

nftables правило матчит `tcp dport { 80, 443, 8080, 8443 }` на выходе из `eth0`. Это ловит:

- Исходящий TCP от sing-box direct-exit inbounds (когда клиент через 2059/2060 просит внешние сайты)
- Исходящий TCP из системных процессов на RuVDS (apt, git и т.д.) — для них DPI-desync тоже применяется, но обычно безвредно

Не ловит:

- wg-туннель к Hetzner (это UDP :51820, не TCP :80/443)
- Трафик существующих не-direct-exit инбаундов (он тоже идёт через wg → Hetzner → уже там уходит наружу)
