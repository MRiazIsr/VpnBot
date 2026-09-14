# Рунбук: переезд с slipstream на Xray XDNS (Hetzner, :53)

Порт 53/udp на Hetzner сейчас занят `slipstream-server` (DNS-туннель, см.
`docs/slipstream-poc.md`): публичный `:53` перенаправлен на локальный `:5300`
через iptables REDIRECT. Xray XDNS-канал (этап 2, `docs/superpowers/specs/
2026-09-09-xray-finalmask-design.md`) слушает VLESS+mKCP с finalmask `xdns`
на том же `:53` — оба сервиса на одном порту одновременно работать не могут.
Зона `t.edgn.net` делегирована на `ns.edgn.net`, который указывает на Hetzner.

Этот рунбук описывает активацию xdns-канала, проверку и откат к slipstream.

## 1. Создать xdns-инбаунд

```bash
curl -sS -X POST https://<API_DOMAIN>:8443/api/inbounds \
  -H "Authorization: Bearer <JWT>" -H "Content-Type: application/json" \
  -d '{
    "protocol": "xdns",
    "tag": "XDNS",
    "display_name": "XDNS (Happ)",
    "listen_port": 53,
    "xdns_domain": "t.edgn.net:txt",
    "xdns_resolvers": "t.edgn.net:txt+udp://8.8.8.8:53,t.edgn.net:txt+udp://1.1.1.1:53",
    "enabled": true
  }'
```

Ключи (`XDNSDecryption`/`XDNSEncryption`, VLESS UUID) не задавать — генерируются
`xray vlessenc` при `setup` (шаг 3).

Список резолверов ориентировочный — реальные рабочие резолверы для РФ подбирает
тестер (см. раздел «Резолверы» ниже); `8.8.8.8`/`1.1.1.1` в РФ заблокированы и
годятся только для проверки с Hetzner/локально.

## 2. Освободить порт 53

Проверить точное действующее правило перед снятием:

```bash
iptables -t nat -S PREROUTING | grep 5300
```

Ожидаемый вывод:

```
-A PREROUTING -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 5300
```

**Записать этот вывод перед следующим шагом — он же откат.**

Остановить slipstream и снять REDIRECT:

```bash
systemctl stop slipstream-server
iptables -t nat -D PREROUTING -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 5300
```

Порядок важен: сначала остановить сервис (чтобы не потерять in-flight
DNS-туннель клиентов), затем снять правило NAT.

## 3. Setup xdns-канала

```bash
curl -sS -X POST https://<API_DOMAIN>:8443/api/xray/xdns/setup \
  -H "Authorization: Bearer <JWT>"
```

`setup` требует свободный порт 53 — если slipstream не остановлен или REDIRECT
не снят (шаг 2), `xray-xdns.service` не сможет забиндиться и упадёт в
`journalctl -u xray-xdns`.

xdns бинднится на конкретный публичный адрес (`GetHetznerServerIP()`), а не
на `0.0.0.0` — это осознанно, чтобы не конфликтовать со стабами
systemd-resolved (`127.0.0.53`, `127.0.0.54`), которые слушают `:53` на
loopback на большинстве stock-Ubuntu. Если `ss -lunp | grep ':53 '` показывает
владельца на НЕ-loopback адресе, отличного от xray, — это реальный конфликт,
освободите порт. Строки на `127.0.0.53`/`127.0.0.54` — норма, их можно
игнорировать (guard `Port53Owner` их и так исключает).

## 4. Проверка

```bash
curl -sS https://<API_DOMAIN>:8443/api/xray/xdns/status \
  -H "Authorization: Bearer <JWT>"
```

Ожидается `"status": "running"`, `"port53_owner": "xray"`.

Проверка резолва с любой внешней точки:

```bash
dig @<hetzner-ip> TXT probe.t.edgn.net
```

Ожидается ответ или `SERVFAIL`, но не таймаут — Xray xdns не обязан отвечать
осмысленно на произвольный запрос, факт ЛЮБОГО ответа (в т.ч. `SERVFAIL`)
подтверждает, что он слушает `:53` и обрабатывает пакеты к зоне; таймаут
означает, что порт не слушается или пакет не доходит.

Финальная проверка — тестер из РФ подключается через Happ по ссылке из кнопки
бота `XDNS (Happ)` и подтверждает реальную связность через операторский
резолвер (см. `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md`,
раздел «Верификация» этапа 2 — ручная проверка из РФ на момент реализации не
проводилась).

**Ссылка не переносит клиентский MTU.** Формат Xray share-link не имеет
параметра под client-side mKCP MTU (только `headerType` и `seed`, который —
ключ обфускации, не MTU — см. `service/vpn.go GenerateXDNSLink`). Спайк
показал, что MTU 900/400 не работает (0 B/s), а MTU 130 — работает. Поэтому
после импорта ссылки в Happ тестер должен вручную зайти в настройки профиля
и выставить mKCP MTU = 130 (то же значение, что `XDNSClientMTU` в
`service/xrayxdns.go`) — иначе соединение не поедет.

## Резолверы

`8.8.8.8` и `1.1.1.1` заблокированы для абонентов в РФ — рабочие резолверы
(операторские DNS или третья сторона, доступная из РФ) подбирает тестер по
месту. Xray xdns сам обходит недоступные резолверы из списка `XDNSResolvers` —
многопутёвость/автопереключение вне скоупа этой задачи.

При смене резолверов конфиг перегенерировать без пересоздания инбаунда:

```bash
curl -sS -X PUT https://<API_DOMAIN>:8443/api/inbounds/<id> \
  -H "Authorization: Bearer <JWT>" -H "Content-Type: application/json" \
  -d '{"xdns_resolvers": "t.edgn.net:txt+udp://<новый резолвер>:53"}'

curl -sS -X POST https://<API_DOMAIN>:8443/api/xray/xdns/reload \
  -H "Authorization: Bearer <JWT>"
```

## Откат к slipstream

```bash
curl -sS -X POST https://<API_DOMAIN>:8443/api/xray/xdns/stop \
  -H "Authorization: Bearer <JWT>"

iptables -t nat -A PREROUTING -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 5300
systemctl start slipstream-server
```

Проверить после отката:

```bash
iptables -t nat -S PREROUTING | grep 5300
systemctl status slipstream-server
```

## Вне скоупа

- Автоматическое (кодовое) отключение slipstream — только операторский шаг
  этого рунбука.
- Мультипуть/несколько резолверов с автопереключением на уровне vpnbot — Xray
  xdns сам ходит через любой доступный резолвер из своего списка.
- Замер резолверного пути из РФ — требует тестера, не проводился на момент
  написания этого рунбука (2026-09-14).
