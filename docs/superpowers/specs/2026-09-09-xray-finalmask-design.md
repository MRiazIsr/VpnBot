# Xray Finalmask как сайдкар к sing-box — Design

Статус: спека утверждена в чате 2026-09-09. MTProto-часть сознательно вне
скоупа.

## Context

Finalmask — слой Xray-core поверх готового потока: ядро отработало VLESS,
Reality и транспорт, а finalmask «переодевает» байты. Задаётся в
`streamSettings.finalmask` на инбаунде и аутбаунде. Три части: `tcp[]`,
`udp[]`, `quicParams`. Маски для TCP: `header-custom`, `fragment`, `sudoku`.
Для UDP: `header-custom`, `mkcp-legacy`, `noise`, `salamander`, `sudoku`,
`xdns`, `xicmp`, `realm`. В ссылке-подписке передаётся параметром `fm` как
JSON всего блока finalmask.

Три факта, которые задают рамки:

1. **Finalmask есть только в Xray-core.** Проект генерирует конфиг sing-box.
   В sing-box, mihomo и клиентах на их базе (Hiddify, Shadowrocket) finalmask
   нет. Применить его можно только добавив Xray рядом с sing-box.
2. **Клиенты.** `fm` понимают только Xray-клиенты: v2rayNG, v2rayN, Happ,
   Streisand. Sing-box-клиент ссылку с `fm` либо не импортирует, либо
   проигнорирует параметр и не подключится к замаскированному порту.
3. **Версия.** `header-custom` и `sudoku` появились в Xray-core v26.3.27.
   В v26.5.9 открыт баг #6184: UDP-листенер с finalmask умирает от первого
   невалидного пакета. Для публичного порта 53 это смертельно. Версия
   пинится только после проверки, что баг закрыт в выбранном релизе.

Что уже есть в репозитории и переиспользуется:

- `service/singboxruvds.go` — паттерн зеркала на RuVDS по SSH: install,
  ensure service, deploy config, start/stop/status/logs.
- `Protocol="shadowtls"` — образец «инбаунд-обёртка, заворачивающая другой
  инбаунд» и в генераторе (`buildInboundGroup`), и в ссылке
  (`generateShadowTLSLink`).
- dnstt/slipstream PoC на Hetzner с делегированной DNS-зоной
  (`docs/slipstream-poc.md`) — образец инфраструктуры для XDNS.

## Решение: Xray как сайдкар, sing-box не трогаем

Рассмотрены три варианта.

1. **Заменить sing-box на Xray целиком.** Отклонён: теряются ShadowTLS,
   anytls-плечо, генератор, тесты и все sing-box-клиенты.
2. **Xray как второй полноценный движок** со своими VLESS-Reality инбаундами
   и синхронизацией пользователей. Рабочий, но дублирует auth, ключи Reality
   и exit-роутинг в двух генераторах. Оставлен как запасной для этапа 1.
3. **Xray как «раздевалка» перед sing-box.** Принят. На RuVDS Xray слушает
   новый порт инбаундом `dokodemo-door` с finalmask, снимает маску и отдаёт
   голые байты в существующий VLESS-Reality инбаунд sing-box на loopback.
   Пользователи, ключи Reality, `ExitOutbound`, zapret остаются в sing-box.
   Xray не знает о пользователях.

Исключение — XDNS. Он работает только поверх mKCP, а mKCP терминируется в
Xray. Поэтому XDNS-инбаунд единственный, где Xray видит список UUID.

### Что сознательно не делаем

- **fragment.** Режет ClientHello клиента. У Reality SNI и так белый,
  резать нечего. Zapret работает на другом плече (egress с RuVDS) и не
  пересекается.
- **XICMP.** Нет IPv6, на Android без root не работает (raw-сокет), ТСПУ
  режет ICMP по частоте.
- **Realm.** Нужен домашний IP в РФ внутри белого списка. Его нет.
- **XDRIVE.** Только анонс.

## Этап 0 — спайк (вне кода проекта)

Цель: ответить на четыре вопроса, а не построить что-то.

1. Работает ли finalmask (`sudoku`, `header-custom`) на инбаунде
   `dokodemo-door`, который форвардит на VLESS-Reality инбаунд sing-box на
   loopback. Клиент — Xray на рабочей машине с `finalmask` на аутбаунде.
2. Каков реальный формат `fm` в ссылке: снять через импорт-экспорт в
   v2rayNG (или из исходников `2dust/AndroidLibXrayLite` / v2rayN).
3. Какая версия Xray-core пинится: минимум v26.3.27, с проверкой статуса
   бага #6184.
4. Матрица клиентов, которым можно выдавать `fm`-ссылки.

Стенд: RuVDS (доступен по SSH), Xray на неиспользуемом порту, целевой
инбаунд — `vless-direct-tcp` :2060 (direct exit, без зависимости от
WireGuard). Hetzner с рабочей машины недоступен, XDNS-часть спайка
переносится на этап 2.

Результат (2026-09-09, `docs/xray-finalmask-spike.md`): dokodemo-door с
finalmask работает, маска обязательна (без неё Reality отвергает
соединение), накладные расходы sudoku около 10 % полосы, на проводе
TLS-заголовков нет. `fm` — URL-encoded JSON блока finalmask. Пин: v26.9.9.
Запасной вариант 2 не нужен. XDNS не замерен: порт 53 занят slipstream,
запасной 5353/udp режет Hetzner Cloud Firewall.

## Этап 1 — TCP-маска на RuVDS

### Модель

Новый `Protocol="mask"` в `InboundConfig`, по образцу shadowtls:

| Поле | Назначение |
|---|---|
| `ListenPort` | публичный порт Xray на RuVDS |
| `MaskInnerTag` | `Tag` внутреннего VLESS-инбаунда sing-box, куда форвардить |
| `MaskJSON` | JSON блока `finalmask` (текст, хранится как есть) |
| `Tag`, `DisplayName`, `Enabled` | как у остальных |

Валидация в handler: `MaskInnerTag` должен указывать на существующий
enabled инбаунд с `Protocol="vless"` и `Transport=""` (TCP). `MaskJSON`
должен парситься как объект с ключом `tcp`. На первом этапе только TCP.

Seed: один disabled инбаунд `RU-MASK` с `sudoku` перед `vless-direct-tcp`,
`MaskJSON` заполняется через API.

### Генерация конфига

`buildXrayConfig(inbounds []InboundConfig) ([]byte, error)` — чистая
функция в `service/xray.go`. Берёт только `Protocol="mask"`, для каждого:

```json
{
  "tag": "<Tag>",
  "listen": "0.0.0.0",
  "port": <ListenPort>,
  "protocol": "dokodemo-door",
  "settings": { "address": "127.0.0.1", "port": <inner.ListenPort>, "network": "tcp" },
  "streamSettings": { "network": "tcp", "finalmask": <MaskJSON> }
}
```

Один аутбаунд `freedom`. Логи в journal. Если mask-инбаундов нет —
возвращает пустой конфиг, и сервис на RuVDS останавливается, а не
перезапускается с пустым набором листенеров.

`GenerateAndReloadRuVDS()` дополняется: после sing-box вызывает
`buildXrayConfig` и `DeployXrayConfigRuVDS`. Ошибка Xray не откатывает
sing-box, а возвращается наверх отдельным сообщением.

### Зеркало на RuVDS

`service/xrayruvds.go` копирует `singboxruvds.go`: `InstallXrayRuVDS`
(скачивание релиза запиненной версии, проверка sha256), `EnsureXrayRuVDSService`
(unit-файл), `DeployXrayConfigRuVDS`, `Start/Stop/IsRunning/Logs`. Версия
— константа `xrayVersion` в этом файле, с комментарием почему именно она.

Роуты: `/api/xray/ruvds/{setup,reload,start,stop,status}` по образцу
singbox-роутов.

### Ссылки

`GenerateLinkForInbound` для `Protocol="mask"`: берёт ссылку внутреннего
инбаунда, подменяет порт на `ListenPort` маски и добавляет `fm=<MaskJSON>`
(URL-encoded). `serverAddr` — RuVDS.

Через `/sub/:token` (Hetzner-направленный) mask-инбаунды не отдаются: Xray
на Hetzner не стоит.

### Бот

Кнопка для mask-инбаунда с пометкой «только v2rayNG / Happ / Streisand».
Текст — в bot-пакете, на русском.

### Тесты

В `service/vpn_test.go` стиле: `buildXrayConfig` с одним и двумя
mask-инбаундами, с пустым набором, с неверным `MaskInnerTag`; ссылка для
mask-инбаунда содержит порт маски и `fm`. Стенд с реальным клиентом —
ручная верификация, как для всех sing-box-изменений.

### Верификация этапа 1

1. `PUT /api/inbounds/<id RU-MASK>` с `{"mask_json": "{\"tcp\":[{\"type\":\"sudoku\",\"settings\":{\"password\":\"<32 случайных символов>\",\"ascii\":\"prefer_entropy\",\"paddingMin\":2,\"paddingMax\":7}}]}", "enabled": true}` → 200.
2. `GET /api/xray/ruvds/config` → JSON с `dokodemo-door`, портом 2071 и `"port": 2060` в settings.
3. `POST /api/xray/ruvds/setup` → 200; `GET /api/xray/ruvds/status` → `installed_version` содержит `26.9.9`, `status: running`.
4. На RuVDS: `ss -ltnp | grep 2071` показывает `xray`; `journalctl -u xray -n 20` без ошибок; `ufw status | grep 2071` — ALLOW.
5. `GET /sub-ruvds/<token>` → в base64 есть строка `vless://…@<RUVDS_IP>:2071?…fm=%7B%22tcp%22…#RU-MASK`; `GET /sub/<token>` этой строки НЕ содержит.
6. v2rayNG (тестер в РФ, инструкция по UI): импорт ссылки → подключение → открыть 2ip.ru, ожидается российский IP RuVDS. На RuVDS `journalctl -u sing-box` показывает `inbound connection from 127.0.0.1`.
7. Hiddify с той же ссылкой НЕ подключается — ожидаемо.
8. `PUT /api/inbounds/<id RU-MASK>/toggle` (без тела — маршрут сам инвертирует `enabled`) → `GET /api/xray/ruvds/status` → `stopped` (DeployXrayConfigRuVDS(nil) остановил сервис).
9. Отключить внутренний инбаунд `vless-direct-tcp` через `PUT /api/inbounds/<id vless-direct-tcp>/toggle` при включённой маске → 400 с перечислением зависимых масок.

Статус: код этапа 1 реализован 2026-09-09 (ветка feature/xray-mask-ruvds), ручная верификация на проде не проводилась.

## Этап 2 — XDNS как бутстрап-канал на Hetzner

Цель: замена dnstt/slipstream в роли канала «достучаться», не основной
транспорт. Сравнение с dnstt по пропускной способности, времени
подключения и деградации после ~10 МБ (эффект, снятый на slipstream).

Прямой путь замерен 09.09.2026 (`docs/xray-finalmask-spike.md`): VLESS+mKCP
через finalmask xdns напрямую в authoritative на Hetzner даёт 540–590 КБ/с
на 10 МБ и НЕ деградирует на втором прогоне — на порядок быстрее
slipstream/dnstt через резолвер и без выгорания. Клиентский MTU 130
обязателен: 400 и 900 не поехали. Осталось замерить путь через резолвер
оператора — только тестер из РФ (публичные 8.8.8.8/1.1.1.1 в РФ
заблокированы). 5353/udp открывается/закрывается через
`POST`/`DELETE /api/network/firewall/rules` самого vpnbot.

Решения зафиксированы в чате 2026-09-14.

### Архитектура — второй экземпляр Xray на Hetzner

XDNS-сервер стоит на выходе, у авторитативного DNS, то есть на Hetzner (там же
vpnbot). Это второй экземпляр Xray, отдельный от RuVDS-сайдкара:

- **RuVDS:** `buildXrayConfig(masks)` → dokodemo-door маски (этап 1, не трогаем).
- **Hetzner:** новый `buildXrayXDNSConfig(xdnsInbounds, users)` → VLESS+mKCP+xdns,
  слушает `:53`. Отдельный конфиг, unit `xray-xdns.service`, отдельный билдер.
  Смешивать с RuVDS-конфигом нельзя — разные хосты.

Xray ставится на Hetzner **локально** (Hetzner до github CDN достаёт, RuVDS нет):
`service/xrayhetzner.go` по образцу `xrayruvds.go`, но локальные команды вместо
SSH. Пин версии тот же (`XrayVersion`, sha256), бинарь `/usr/local/bin/xray`
общий, конфиг `/etc/xray-xdns/config.json`.

### Инфраструктура и порт 53

Живая зона `t.edgn.net` NS → `ns.edgn.net` → Hetzner. Порт 53 забираем у
slipstream (решение 2026-09-14): останавливаем slipstream, снимаем
`REDIRECT 53 → 5300`, XDNS биндит 53 и обслуживает готовую зону `t.edgn.net`
— новая NS-делегация не нужна. Освобождение 53 — операторский шаг в рунбуке,
НЕ в коде: убивать чужой сервис из кода опасно; `setup` XDNS просто упадёт с
понятной ошибкой, если 53 занят. Откат: вернуть REDIRECT + поднять slipstream.

Резолверы в ссылке — поле инбаунда, не хардкод: какой резолвер работает в РФ,
знает только тестер. 8.8.8.8 и 1.1.1.1 в РФ заблокированы, годятся лишь для
проверки извне; боевой — операторский.

VLESS без TLS на публичный адрес Xray не поднимает: нужен `xray vlessenc`, пара
`decryption` (сервер) / `encryption` (клиент). Берётся короткий X25519-вариант,
НЕ ML-KEM (тот 1.5 КБ в ссылке). Пара генерится при `setup`, хранится в полях.

### Конфиг

Xray на Hetzner локально (без SSH), unit `xray-xdns.service`. Инбаунд:

```json
{
  "protocol": "vless",
  "settings": { "clients": [ { "id": "<UUID>" } ], "decryption": "mlkem768x25519plus.native.600s.<x25519-key>" },
  "streamSettings": {
    "network": "kcp",
    "kcpSettings": { "mtu": 900 },
    "finalmask": { "udp": [ { "type": "xdns", "settings": { "domains": ["t.edgn.net:txt"] } } ] }
  }
}
```

Список `clients` — из активных `User`. Аутбаунд `freedom`, egress direct с
Hetzner. MTU 900 сервер / 130 клиент — константы (спайком доказано: клиентский
130 обязателен, 400 и 900 не поехали).

### Модель — `Protocol="xdns"`

Новые поля `InboundConfig`:

| Поле | Назначение |
|---|---|
| `ListenPort` | 53 |
| `XDNSDomain` | авторитативный домен с методом, напр. `t.edgn.net:txt` |
| `XDNSResolvers` | клиентские резолверы для ссылки, список через запятую: `t.edgn.net:txt+udp://<resolver>:53,...` |
| `XDNSDecryption` | серверный VLESS-ключ (`decryption`), генерится при setup |
| `XDNSEncryption` | клиентский VLESS-ключ (`encryption`), уходит в ссылку |

Генератор: новый `buildXrayXDNSConfig(xdnsInbounds, users)` (отдельно от
RuVDS-билдера). Деплой: `service/xrayhetzner.go` — локальная запись
`/etc/xray-xdns/config.json` и `systemctl restart xray-xdns` (Xray не умеет
hot-reload). Билдер-паттерн как у RuVDS: `.new` → `xray -test -format json` →
`cmp` → `mv` → restart-if-active.

### Ссылка и раздача

`vless://<uuid>@<резолвер>:53?type=kcp&encryption=<enc>&fm=<xdns-блок>#XDNS-<user>`,
адрес назначения — резолвер (первый из `XDNSResolvers`), не Hetzner. Клиентский
`fm` = `{"udp":[{"type":"xdns","settings":{"resolvers":[...]}}]}`. Только кнопкой
`XDNS (Happ)` в боте; в подписку `/sub`/`/sub-ruvds` НЕ попадает (как маска).

### API

`/api/xray/xdns/{setup,reload,start,stop,status,config,logs}` — Hetzner-локальные,
по образцу singbox/ruvds-роутов.

### Верификация

Юниты на `buildXrayXDNSConfig` и билдер xdns-ссылки. Ручная: `setup` на Hetzner
(после освобождения 53), `xray-xdns` слушает `:53`, `dig` к домену отвечает;
тестер из РФ с Happ через операторский резолвер. Прямой путь замерен
(540–590 КБ/с), резолверный — открытый вопрос, закрывает тестер.

## Этап 3 — бэклог

- UDP-маски и port hopping через `quicParams`, если у Xray подтверждается
  Hysteria2-инбаунд.
- Маска для Hetzner-направленных ссылок, если появится необходимость.

## Риски

- **Баг #6184** закрыт 2026-05-29, в v26.9.9 его нет.
- **Dokodemo-door + finalmask** подтверждён спайком.
- **Hetzner Cloud Firewall** режет нестандартные UDP-порты, для XDNS нужно
  правило в облачном файрволе — открывается через
  `POST /api/network/firewall/rules` (проверено 09.09.2026 на 5353/udp).
- **Раздвоение клиентской базы.** Часть ссылок только для Xray-клиентов.
  Решается пометкой в боте, а не автодетектом.
- **Второй бинарь на RuVDS.** Пин версии, sha256, unit с `Restart=always`.
  При cold reboot RuVDS (см. память о rollback-схеме) Xray поднимается
  systemd, конфиг лежит на диске.
