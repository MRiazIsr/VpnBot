# Xray Finalmask — результаты спайка (2026-09-09)

Этап 0 из `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md`.
Стенд был одноразовым: свои ключи, свои порты, всё снесено после замеров.

## Вопрос 1. Работает ли finalmask на dokodemo-door перед sing-box — ДА

Стенд на RuVDS (87.247.157.120):

```
клиент Xray 26.9.9 ─sudoku─▶ :2071 Xray dokodemo-door + finalmask ─▶ 127.0.0.1:2072 sing-box VLESS Reality (vision) ─▶ direct
                              :2074 тот же sing-box напрямую (baseline, без Xray)
```

Конфиг инбаунда, который принял Xray:

```json
{"listen":"0.0.0.0","port":2071,"protocol":"dokodemo-door",
 "settings":{"address":"127.0.0.1","port":2072,"network":"tcp"},
 "streamSettings":{"network":"tcp","finalmask":{"tcp":[{"type":"sudoku",
   "settings":{"password":"…","ascii":"prefer_entropy","paddingMin":2,"paddingMax":7}}]}}}
```

Что подтверждено:

- Reality-рукопожатие проходит через маску, sing-box видит соединение с
  127.0.0.1 и отдаёт трафик в свой outbound. Пользователи, ключи Reality и
  роутинг остаются в sing-box, Xray о них не знает.
- Клиент **без** маски на замаскированный порт получает от sing-box
  `REALITY: processed invalid connection` — маска обязательна, а не
  опциональна.
- На проводе: за 3 сессии к :2074 tcpdump нашёл пакеты с TLS-заголовком
  `16 03` в начале payload, к :2071 — ни одного. Первый data-пакет к :2071
  7616 байт вместо 437 (sudoku склеивает и паддит).

Замер, 10 МБ с mirror.yandex.ru, клиент вне РФ, три прогона:

| путь | МБ/с |
|---|---|
| sing-box напрямую (:2074) | 1.63 / 1.63 / 1.69 |
| через sudoku (:2071) | 1.47 / 1.48 / 1.53 |

Накладные расходы sudoku около 10 % по полосе. CPU не мерил, стенд короткий.

Побочное наблюдение: прямой egress с RuVDS работает только с bind на
87.247.157.120. С 194.87.80.237 ответы не приходят (вход на этот адрес
заблокирован, см. память о тройном backhaul). Для сайдкара это не важно:
egress делает sing-box.

## Вопрос 2. Формат `fm` в ссылке — JSON блока finalmask, URL-encoded

Снято из исходников клиентов:

- v2rayN (`ServiceLib/Handler/Fmt/BaseFmt.cs`): при экспорте JSON
  сериализуется компактно и кладётся в `fm` через `UrlEncode`; при импорте
  `fm` декодируется и парсится как JSON.
- v2rayNG (`fmt/FmtBase.kt`): `config.finalMask = queryParam["fm"]` и
  обратно, без преобразований.

Значит ссылка: `vless://…?security=reality&…&fm=%7B%22tcp%22%3A%5B…%5D%7D#tag`.
Значение `fm` — ровно тот блок, что лежит в `streamSettings.finalmask`
у клиента.

## Вопрос 3. Версия — v26.9.9

- Все релизы Xray помечены prerelease, это норма проекта.
- `header-custom` и `sudoku` есть с v26.3.27.
- Баг #6184 (UDP-листенер с finalmask умирает от первого мусорного пакета)
  закрыт 2026-05-29, то есть в v26.6.1 и новее.
- Спайк проведён на v26.9.9 (2026-09-08), sha256 `Xray-linux-64.zip`
  начинается с `1eb9175d0f0a8f81`. Пинить её.

## Вопрос 4. Клиенты для `fm`-ссылок

v2rayNG, v2rayN, Happ, Streisand (все на Xray-core). Sing-box-клиенты
(Hiddify, sing-box для iOS), Shadowrocket и mihomo-клиенты finalmask не
понимают, у mihomo открыт issue #2604. Ссылки с `fm` выдавать только с
пометкой в боте.

## XDNS (этап 2) — не замерен, два блокера

Что выяснилось про инфраструктуру на Hetzner:

- Живая делегированная зона — `t.edgn.net` → `ns.edgn.net` → Hetzner
  (49.13.201.110). Зона `e.moskva.live` из `docs/slipstream-poc.md` больше
  не существует (NXDOMAIN у регистратора).
- Порт 53/udp занят slipstream: `iptables -t nat` REDIRECT 53 → 5300, счётчик
  1.4 млн пакетов — канал используется. Публичный `:53` без редиректа
  свободен.
- Рекурсия через 8.8.8.8 до Hetzner доходит (SERVFAIL от slipstream на
  чужой запрос). Для тестера в РФ это не значит ничего: 8.8.8.8 и 1.1.1.1
  там заблокированы, ходить придётся через DNS оператора.
- Xray 26.9.9 отказывается поднимать VLESS-аутбаунд без TLS или VLESS
  encryption на публичный IP. Для XDNS без TLS нужен `xray vlessenc` и пара
  `decryption`/`encryption` (X25519-вариант короткий, ML-KEM — 1.5 КБ в
  ссылке).

Что не удалось:

1. Снять редирект 53 → 5300 на время теста — автоматика это не сделала,
   и правильно: канал живой, решение за владельцем.
2. Обходной вариант на 5353/udp: до Hetzner не дошёл ни один пакет
   (tcpdump пуст), ufw был открыт. Почти наверняка Hetzner Cloud Firewall,
   тот же блокер, что в slipstream-PoC. Проверить можно через
   `/api/network/firewall` или консоль Hetzner.

Для замера XDNS нужно одно из двух: окно, когда slipstream можно снять с
53, или правило 5353/udp в Cloud Firewall. Конфиги для стенда лежат в
`/opt/xray-spike/` на Hetzner (бинарь v26.9.9 и `xdns.json`, ключи
одноразовые), можно переиспользовать.

## Решение по этапу 1

Вариант «Xray как раздевалка перед sing-box» подтверждён, запасной вариант
с копией ключей не нужен. План реализации —
`docs/superpowers/plans/2026-09-09-xray-mask-ruvds.md`.
